package filestore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Karlmit/Klaras-Library/internal/filestore"
	"github.com/Karlmit/Klaras-Library/internal/store"
	"github.com/Karlmit/Klaras-Library/internal/testdb"
)

func TestSanitiseComponentKeepsSwedish(t *testing.T) {
	cases := []struct{ in, want, why string }{
		{"Susanne Åkesson", "Susanne Åkesson", "Swedish letters are letters, not accents to strip"},
		{"Bergström", "Bergström", "ö must survive"},
		{"A/B: Test", "A B Test", "path separators and colons are replaced"},
		{"trailing dots...", "trailing dots", "SMB dislikes trailing dots"},
		{"  spaced   out  ", "spaced out", "whitespace collapses"},
		{"", "Unknown", "empty gets a placeholder"},
		{"CON", "CON_", "reserved on Windows, and this is served over SMB"},
		{"a\x00b", "a b", "control characters are removed"},
	}
	for _, c := range cases {
		if got := filestore.SanitiseComponent(c.in); got != c.want {
			t.Errorf("SanitiseComponent(%q) = %q, want %q (%s)", c.in, got, c.want, c.why)
		}
	}
}

func TestSanitiseTruncatesOnBytesNotRunes(t *testing.T) {
	// Swedish characters are two bytes in UTF-8, so a rune-based limit would
	// still produce a component over the filesystem's 255-byte cap.
	long := ""
	for i := 0; i < 200; i++ {
		long += "ö"
	}
	got := filestore.SanitiseComponent(long)
	if len(got) > 120 {
		t.Errorf("component is %d bytes, want <= 120", len(got))
	}
	// And it must still be valid UTF-8, not a split rune.
	for _, r := range got {
		if r == '�' {
			t.Error("truncation split a multi-byte character")
		}
	}
}

func TestTemplateLayout(t *testing.T) {
	tpl := filestore.DefaultTemplate()
	idx := 3.0

	plain := tpl.Dir(filestore.Meta{ID: 1, Title: "Röda Rummet", AuthorSort: "Strindberg, August"})
	if plain != filepath.Join("Strindberg, August", "Röda Rummet") {
		t.Errorf("plain layout = %q", plain)
	}

	series := tpl.Dir(filestore.Meta{
		ID: 2, Title: "Isprinsessan", AuthorSort: "Läckberg, Camilla",
		Series: "Fjällbacka", SeriesIndex: &idx,
	})
	if series != filepath.Join("Läckberg, Camilla", "Fjällbacka", "03 - Isprinsessan") {
		t.Errorf("series layout = %q", series)
	}

	if base := tpl.FileBase(filestore.Meta{Title: "Röda Rummet", AuthorSort: "Strindberg, August"}); base != "Röda Rummet - Strindberg, August" {
		t.Errorf("file base = %q", base)
	}
}

func TestIsSafeRelativeRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "/abs/path", "../escape", "a/../../escape", "a/\x00b"} {
		if filestore.IsSafeRelative(bad) {
			t.Errorf("IsSafeRelative(%q) = true; this would let a metadata edit write outside the library", bad)
		}
	}
	for _, ok := range []string{"Author/Title", "Åkesson, Susanne/Bok"} {
		if !filestore.IsSafeRelative(ok) {
			t.Errorf("IsSafeRelative(%q) = false, want true", ok)
		}
	}
}

// --- integration ------------------------------------------------------------

type fx struct {
	st   *filestore.Store
	pool *pgxpool.Pool
	root string
	id   int64
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := testdb.For(t, os.Getenv("KLARAS_TEST_DATABASE_URL"), "filestore")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	if err := store.Migrate(ctx, dsn, log); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, dsn, 4, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	if _, err := db.Pool.Exec(ctx, `
		TRUNCATE books, authors, book_authors, book_files, file_operations
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	oldDir := filepath.Join(root, "Old Author", "Old Title")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"book.epub", "cover.jpg"} {
		if err := os.WriteFile(filepath.Join(oldDir, f), []byte("content of "+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var id int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO books (uuid, title, author_sort, path)
		VALUES (gen_random_uuid(), 'Röda Rummet', 'Strindberg, August', 'Old Author/Old Title')
		RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO book_files (book_id, format, filename, size_bytes)
		VALUES ($1,'EPUB','book.epub',20)`, id); err != nil {
		t.Fatal(err)
	}

	return &fx{
		st:   filestore.New(root, filestore.DefaultTemplate(), db.Pool, log),
		pool: db.Pool, root: root, id: id,
	}
}

func TestApplyMovesFilesAndUpdatesDatabase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	plan, err := f.st.PlanFor(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("Strindberg, August", "Röda Rummet")
	if plan.ToDir != want {
		t.Fatalf("plan targets %q, want %q", plan.ToDir, want)
	}
	if err := f.st.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}

	// Files landed, with Swedish characters intact on disk.
	newFile := filepath.Join(f.root, want, "Röda Rummet - Strindberg, August.epub")
	if _, err := os.Stat(newFile); err != nil {
		t.Errorf("moved file not found at %s: %v", newFile, err)
	}
	if _, err := os.Stat(filepath.Join(f.root, want, "cover.jpg")); err != nil {
		t.Error("cover did not travel with the book")
	}
	// The old directory is gone, not left behind empty.
	if _, err := os.Stat(filepath.Join(f.root, "Old Author")); !os.IsNotExist(err) {
		t.Error("empty source directory was not pruned")
	}

	var dbPath, dbName string
	if err := f.pool.QueryRow(ctx,
		`SELECT b.path, f.filename FROM books b JOIN book_files f ON f.book_id=b.id WHERE b.id=$1`,
		f.id).Scan(&dbPath, &dbName); err != nil {
		t.Fatal(err)
	}
	if dbPath != want {
		t.Errorf("books.path = %q, want %q", dbPath, want)
	}
	if dbName != "Röda Rummet - Strindberg, August.epub" {
		t.Errorf("book_files.filename = %q", dbName)
	}

	// The journal closed cleanly.
	var open int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM file_operations WHERE state IN ('pending','staged')`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("%d journal entries left open after a successful move", open)
	}
}

// TestReconcileAfterCrashBetweenMoveAndCommit simulates the exact failure the
// journal exists for: the file reached its destination but the process died
// before the database was updated.
func TestReconcileAfterCrashBetweenMoveAndCommit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	oldAbs := filepath.Join(f.root, "Old Author", "Old Title", "book.epub")
	newDir := filepath.Join(f.root, "Strindberg, August", "Röda Rummet")
	newAbs := filepath.Join(newDir, "Röda Rummet - Strindberg, August.epub")

	// Journal the intent, do the move, then stop -- as a crash would.
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO file_operations (book_id, op, src, dst, state)
		VALUES ($1,'move',$2,$3,'staged')`, f.id, oldAbs, newAbs); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldAbs, newAbs); err != nil {
		t.Fatal(err)
	}

	rep, err := f.st.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Completed != 1 {
		t.Errorf("reconcile completed %d operations, want 1 (report: %+v)", rep.Completed, rep)
	}

	var dbPath, dbName string
	if err := f.pool.QueryRow(ctx,
		`SELECT b.path, fl.filename FROM books b JOIN book_files fl ON fl.book_id=b.id WHERE b.id=$1`,
		f.id).Scan(&dbPath, &dbName); err != nil {
		t.Fatal(err)
	}
	if dbPath != filepath.Join("Strindberg, August", "Röda Rummet") {
		t.Errorf("database still points at %q after recovery", dbPath)
	}
	if dbName != "Röda Rummet - Strindberg, August.epub" {
		t.Errorf("filename not recovered: %q", dbName)
	}
}

// TestReconcileWhenMoveNeverStarted must leave everything alone.
func TestReconcileWhenMoveNeverStarted(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	oldAbs := filepath.Join(f.root, "Old Author", "Old Title", "book.epub")
	newAbs := filepath.Join(f.root, "Strindberg, August", "Röda Rummet", "x.epub")
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO file_operations (book_id, op, src, dst, state)
		VALUES ($1,'move',$2,$3,'pending')`, f.id, oldAbs, newAbs); err != nil {
		t.Fatal(err)
	}

	rep, err := f.st.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RolledBack != 1 {
		t.Errorf("rolled back %d, want 1 (report: %+v)", rep.RolledBack, rep)
	}
	var dbPath string
	if err := f.pool.QueryRow(ctx, `SELECT path FROM books WHERE id=$1`, f.id).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	if dbPath != "Old Author/Old Title" {
		t.Errorf("path changed to %q even though the move never happened", dbPath)
	}
	if _, err := os.Stat(oldAbs); err != nil {
		t.Error("source file was disturbed by a move that never ran")
	}
}

// TestReconcileWithBothCopiesPresent covers an interrupted cross-filesystem copy.
func TestReconcileWithBothCopiesPresent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	oldAbs := filepath.Join(f.root, "Old Author", "Old Title", "book.epub")
	newDir := filepath.Join(f.root, "Strindberg, August", "Röda Rummet")
	newAbs := filepath.Join(newDir, "Röda Rummet - Strindberg, August.epub")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(oldAbs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newAbs, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := f.pool.Exec(ctx, `
		INSERT INTO file_operations (book_id, op, src, dst, sha256, state)
		VALUES ($1,'move',$2,$3,$4,'staged')`,
		f.id, oldAbs, newAbs, sha256Of(data)); err != nil {
		t.Fatal(err)
	}

	rep, err := f.st.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Duplicates != 1 {
		t.Errorf("resolved %d duplicates, want 1 (report: %+v)", rep.Duplicates, rep)
	}
	if _, err := os.Stat(oldAbs); !os.IsNotExist(err) {
		t.Error("source still present after the destination was verified")
	}
	if _, err := os.Stat(newAbs); err != nil {
		t.Error("verified destination was removed")
	}
}

// TestReconcileFlagsLostFiles is the one case that needs a human.
func TestReconcileFlagsLostFiles(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.pool.Exec(ctx, `
		INSERT INTO file_operations (book_id, op, src, dst, state)
		VALUES ($1,'move',$2,$3,'staged')`,
		f.id, filepath.Join(f.root, "gone", "a.epub"), filepath.Join(f.root, "also-gone", "b.epub")); err != nil {
		t.Fatal(err)
	}

	rep, err := f.st.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Lost != 1 {
		t.Errorf("recorded %d lost, want 1 (report: %+v)", rep.Lost, rep)
	}
	var flagged bool
	var reasons []string
	if err := f.pool.QueryRow(ctx,
		`SELECT needs_review, review_reasons FROM books WHERE id=$1`, f.id).
		Scan(&flagged, &reasons); err != nil {
		t.Fatal(err)
	}
	if !flagged {
		t.Error("book was not flagged for review after both copies went missing")
	}
	var found bool
	for _, r := range reasons {
		if r == "file_lost_during_move" {
			found = true
		}
	}
	if !found {
		t.Errorf("review reasons = %v, want file_lost_during_move", reasons)
	}
}

func sha256Of(b []byte) []byte {
	h := sha256.New()
	h.Write(b)
	return h.Sum(nil)
}

// TestReorganizeIsReversible is the safety net behind the one operation that
// touches the whole library at once.
//
// Reorganize renames every book folder, and on the library this was built
// against that is 28,000 directories in one go. The journal records src and dst
// for each move, so "what if the layout is wrong" should be answerable with a
// command rather than with a restore from backup.
func TestReorganizeIsReversible(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	originalDir := filepath.Join(f.root, "Old Author", "Old Title")
	originalFile := filepath.Join(originalDir, "book.epub")
	before, err := os.ReadFile(originalFile)
	if err != nil {
		t.Fatal(err)
	}

	// Everything after this instant is what a revert should walk back.
	mark := time.Now().Add(-time.Second)

	plan, err := f.st.PlanFor(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(originalFile); !os.IsNotExist(err) {
		t.Fatal("setup failed: the file did not move")
	}

	// A dry run must report the work without doing any of it.
	dry, err := f.st.Revert(ctx, mark, true, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Candidates == 0 {
		t.Fatal("dry run found no moves to undo")
	}
	if dry.Reverted != 0 {
		t.Errorf("dry run undid %d moves; it must change nothing", dry.Reverted)
	}
	if _, err := os.Stat(originalFile); !os.IsNotExist(err) {
		t.Error("dry run moved a file")
	}

	rep, err := f.st.Revert(ctx, mark, false, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reverted == 0 {
		t.Fatal("revert undid nothing")
	}
	if rep.Failed != 0 {
		t.Errorf("%d reverts failed", rep.Failed)
	}

	// The file is back, byte for byte.
	after, err := os.ReadFile(originalFile)
	if err != nil {
		t.Fatalf("file did not come back to %s: %v", originalFile, err)
	}
	if string(after) != string(before) {
		t.Error("file contents changed across the round trip")
	}

	// And so is the database.
	var dbPath, dbName string
	if err := f.pool.QueryRow(ctx,
		`SELECT b.path, fl.filename FROM books b JOIN book_files fl ON fl.book_id=b.id WHERE b.id=$1`,
		f.id).Scan(&dbPath, &dbName); err != nil {
		t.Fatal(err)
	}
	if dbPath != "Old Author/Old Title" {
		t.Errorf("books.path = %q after revert, want the original", dbPath)
	}
	if dbName != "book.epub" {
		t.Errorf("filename = %q after revert, want book.epub", dbName)
	}

	// Running it twice must be safe.
	again, err := f.st.Revert(ctx, mark, false, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if again.Reverted != 0 {
		t.Errorf("a second revert undid %d more moves; it should be a no-op", again.Reverted)
	}
}

// TestRevertRespectsTheSinceBoundary makes sure one reorganize can be undone
// without disturbing earlier, deliberate moves.
func TestRevertRespectsTheSinceBoundary(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	plan, err := f.st.PlanFor(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}

	// A boundary set after the move: nothing should be in scope.
	rep, err := f.st.Revert(ctx, time.Now().Add(time.Minute), false, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Candidates != 0 || rep.Reverted != 0 {
		t.Errorf("revert reached back past --since: %d candidates, %d undone",
			rep.Candidates, rep.Reverted)
	}
	moved := filepath.Join(f.root, "Strindberg, August", "Röda Rummet",
		"Röda Rummet - Strindberg, August.epub")
	if _, err := os.Stat(moved); err != nil {
		t.Error("a move outside the --since window was undone anyway")
	}
}

func TestDeleteBookFilesRemovesTheDirectory(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	dir := filepath.Join(f.root, "Old Author", "Old Title")
	res, err := f.st.DeleteBookFiles(ctx, f.id, "Old Author/Old Title", []string{"book.epub"})
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesRemoved < 2 {
		t.Errorf("removed %d files, want the epub and the cover", res.FilesRemoved)
	}
	if !res.DirRemoved {
		t.Error("the empty book directory was left behind")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("book directory still exists")
	}
	// The author folder had nothing else in it, so it goes too.
	if _, err := os.Stat(filepath.Join(f.root, "Old Author")); !os.IsNotExist(err) {
		t.Error("empty author directory was not pruned")
	}
	// And the library root itself must survive.
	if _, err := os.Stat(f.root); err != nil {
		t.Fatal("the library root was removed")
	}
}

// TestDeleteLeavesDirectoriesHoldingOtherFiles is the safety property: a
// directory the user has put something else into is never removed.
func TestDeleteLeavesDirectoriesHoldingOtherFiles(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	dir := filepath.Join(f.root, "Old Author", "Old Title")
	stray := filepath.Join(dir, "notes-i-wrote.txt")
	if err := os.WriteFile(stray, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := f.st.DeleteBookFiles(ctx, f.id, "Old Author/Old Title", []string{"book.epub"})
	if err != nil {
		t.Fatal(err)
	}
	if res.DirRemoved {
		t.Error("removed a directory that still held a file the library does not own")
	}
	if _, err := os.Stat(stray); err != nil {
		t.Error("an unrelated file was deleted")
	}
}

// TestDeleteIsJournalled: a delete leaves the same audit trail as a move.
func TestDeleteIsJournalled(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.st.DeleteBookFiles(ctx, f.id, "Old Author/Old Title", []string{"book.epub"}); err != nil {
		t.Fatal(err)
	}
	var op, state string
	if err := f.pool.QueryRow(ctx,
		`SELECT op, state FROM file_operations WHERE book_id=$1 ORDER BY id DESC LIMIT 1`,
		f.id).Scan(&op, &state); err != nil {
		t.Fatal(err)
	}
	if op != "delete" || state != "done" {
		t.Errorf("journal shows op=%q state=%q, want delete/done", op, state)
	}
}

// TestDryRunPlansTheSameDirectoriesItWillUse is the guard for a review artefact
// that lied.
//
// Two books with the same title and author render to one directory, and Apply
// disambiguates with a " (id)" suffix at move time. The dry run did not: it
// printed the unresolved target, so a plan covering this library showed 327
// pairs of books being merged into single folders with colliding filenames.
// Nothing would actually have been lost, but a plan an operator cannot trust is
// worse than no plan -- the whole point is to review what will happen.
func TestDryRunPlansTheSameDirectoriesItWillUse(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// A second book, same title and author, in its own Calibre-style folder.
	second := filepath.Join(f.root, "Old Author", "Old Title Duplicate")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "book.epub"), []byte("the other copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	var id2 int64
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO books (uuid, title, author_sort, path)
		VALUES (gen_random_uuid(), 'Röda Rummet', 'Strindberg, August', 'Old Author/Old Title Duplicate')
		RETURNING id`).Scan(&id2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO book_files (book_id, format, filename, size_bytes)
		VALUES ($1,'EPUB','book.epub',14)`, id2); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	var planned bytes.Buffer
	if _, err := f.st.Reorganize(ctx, true, &planned, log); err != nil {
		t.Fatal(err)
	}
	dirsPlanned := destinations(planned.String())
	if len(dirsPlanned) != 2 {
		t.Fatalf("planned %d destinations, want 2: %v", len(dirsPlanned), dirsPlanned)
	}
	if dirsPlanned[0] == dirsPlanned[1] {
		t.Fatalf("dry run put both books in %q; Apply would not", dirsPlanned[0])
	}

	// Now do it for real and compare with what was promised.
	if _, err := f.st.Reorganize(ctx, false, io.Discard, log); err != nil {
		t.Fatal(err)
	}
	var actual []string
	rows, err := f.pool.Query(ctx, `SELECT path FROM books ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		actual = append(actual, p)
	}
	for i := range actual {
		if actual[i] != dirsPlanned[i] {
			t.Errorf("book %d: dry run said %q, the move used %q",
				i, dirsPlanned[i], actual[i])
		}
	}

	// And both books' files survived, which is what the folders were for.
	for _, dir := range actual {
		if _, err := os.Stat(filepath.Join(f.root, dir, "Röda Rummet - Strindberg, August.epub")); err != nil {
			t.Errorf("no epub in %s: %v", dir, err)
		}
	}
}

// destinations pulls the "to" lines out of a reorganize plan.
func destinations(plan string) []string {
	var out []string
	for _, line := range strings.Split(plan, "\n") {
		if rest, ok := strings.CutPrefix(line, "    to "); ok {
			out = append(out, rest)
		}
	}
	return out
}

// TestCaseOnlyDifferenceIsFiledApart keeps the tree portable.
//
// Two books whose titles differ only in capitalisation render to directories a
// case-sensitive filesystem keeps apart and Windows, macOS and exFAT do not.
// Left alone, copying the library to any of those merges the folders and one
// book's files overwrite the other's. This library has eleven such pairs.
func TestCaseOnlyDifferenceIsFiledApart(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	shouty := filepath.Join(f.root, "Old Author", "Shouty")
	if err := os.MkdirAll(shouty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shouty, "book.epub"), []byte("the loud one"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Same author, same title, different case: "RÖDA RUMMET" vs "Röda Rummet".
	var id2 int64
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO books (uuid, title, author_sort, path)
		VALUES (gen_random_uuid(), 'RÖDA RUMMET', 'Strindberg, August', 'Old Author/Shouty')
		RETURNING id`).Scan(&id2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO book_files (book_id, format, filename, size_bytes)
		VALUES ($1,'EPUB','book.epub',12)`, id2); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := f.st.Reorganize(ctx, false, io.Discard, log); err != nil {
		t.Fatal(err)
	}

	var paths []string
	rows, err := f.pool.Query(ctx, `SELECT path FROM books ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 books, got %v", paths)
	}
	if strings.EqualFold(paths[0], paths[1]) {
		t.Errorf("both books filed at %q ignoring case; on Windows or macOS one "+
			"would overwrite the other", paths[0])
	}

	// Both survived on disk.
	for _, p := range paths {
		entries, err := os.ReadDir(filepath.Join(f.root, p))
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		if len(entries) == 0 {
			t.Errorf("%s is empty", p)
		}
	}
}

// TestApplyRefusesWhenASourceFileIsMissing guards the failure that broke eight
// books during the real reorganize.
//
// journalledMove treats a missing source as nothing to do, which is right when
// re-running after a crash. Apply then went on to rewrite books.path and
// book_files.filename anyway, so the row described a location the file had
// never reached -- and the run reported no failures. Calibre truncates long
// filenames and can leave a trailing space before the extension that the
// imported name does not carry, so "the file is not where the row says" is a
// real state, not a hypothetical.
func TestApplyRefusesWhenASourceFileIsMissing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// The recorded name gains a space the file on disk does not have.
	if _, err := f.pool.Exec(ctx,
		`UPDATE book_files SET filename='book .epub' WHERE book_id=$1`, f.id); err != nil {
		t.Fatal(err)
	}

	plan, err := f.st.PlanFor(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.Apply(ctx, plan); err == nil {
		t.Fatal("Apply succeeded with a source file that does not exist")
	}

	var path string
	var review bool
	if err := f.pool.QueryRow(ctx,
		`SELECT path, needs_review FROM books WHERE id=$1`, f.id).Scan(&path, &review); err != nil {
		t.Fatal(err)
	}
	if path != "Old Author/Old Title" {
		t.Errorf("book moved to %q despite the failure; it must stay where its files are", path)
	}
	if !review {
		t.Error("book was not flagged for review")
	}
	// The real file is untouched.
	if _, err := os.Stat(filepath.Join(f.root, "Old Author", "Old Title", "book.epub")); err != nil {
		t.Errorf("original file disturbed: %v", err)
	}
}

// TestRelinkFindsAFileRenamedOutFromUnderTheDatabase repairs the state above.
//
// Matching is by size and extension, because the name is the thing that is
// wrong.
func TestRelinkFindsAFileRenamedOutFromUnderTheDatabase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// setup wrote "content of book.epub" -- 20 bytes.
	want := int64(len("content of book.epub"))
	if _, err := f.pool.Exec(ctx,
		`UPDATE book_files SET filename='book .epub', size_bytes=$2 WHERE book_id=$1`,
		f.id, want); err != nil {
		t.Fatal(err)
	}

	missing, err := f.st.Missing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 {
		t.Fatalf("Missing() found %d absent files, want 1", len(missing))
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rep, err := f.st.Relink(ctx, false, io.Discard, log)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Relinked != 1 || rep.Ambiguous != 0 || rep.Unresolved != 0 {
		t.Fatalf("relink report: %+v, want 1 relinked and nothing else", rep)
	}

	var name string
	if err := f.pool.QueryRow(ctx,
		`SELECT filename FROM book_files WHERE book_id=$1`, f.id).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "book.epub" {
		t.Errorf("filename = %q, want the name actually on disk", name)
	}

	after, err := f.st.Missing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("still missing after relink: %+v", after)
	}
}

// TestSlashInMetadataDoesNotCreateDirectories guards the template's shape.
//
// Dir splits the rendered template on "/" to find its components, so a slash in
// an author or title used to become a directory separator: "Agnes Wold /
// Cecilia Chrapkowska" filed a book two levels deep under a two-level template,
// and "Sveriges statsministrar under 100 år / Samlingsvolym" split the title in
// half. About seventy books in the real library. FileBase never showed it,
// because it sanitises the whole assembled string rather than its parts.
func TestSlashInMetadataDoesNotCreateDirectories(t *testing.T) {
	tpl := filestore.DefaultTemplate()

	cases := []struct {
		name  string
		meta  filestore.Meta
		depth int
	}{
		{"slash in author", filestore.Meta{
			ID: 1, Title: "Praktika för blivande föräldrar",
			AuthorSort: "Wold, Agnes/Chrapkowska, Cecilia"}, 2},
		{"slash in title", filestore.Meta{
			ID: 2, Title: "Sveriges statsministrar under 100 år / Samlingsvolym",
			AuthorSort: "Ohlsson, Per T"}, 2},
		{"backslash in author", filestore.Meta{
			ID: 3, Title: "Skolans kriser",
			AuthorSort: `Westberg, Joakim Landahl\David Sjögren`}, 2},
		{"slash in series", filestore.Meta{
			ID: 4, Title: "Kebab varje dag", AuthorSort: "Samuelsson, Åke",
			Series: "Munken / Kulan", SeriesIndex: ptrf(2)}, 3},
	}

	for _, c := range cases {
		dir := tpl.Dir(c.meta)
		if got := strings.Count(dir, string(filepath.Separator)) + 1; got != c.depth {
			t.Errorf("%s: Dir = %q, %d components, want %d",
				c.name, dir, got, c.depth)
		}
		for _, part := range strings.Split(dir, string(filepath.Separator)) {
			if part == "" || part == "." || part == ".." {
				t.Errorf("%s: Dir = %q has an empty or traversing component", c.name, dir)
			}
		}
		if !filestore.IsSafeRelative(dir) {
			t.Errorf("%s: Dir = %q is not a safe relative path", c.name, dir)
		}
	}
}

func ptrf(v float64) *float64 { return &v }
