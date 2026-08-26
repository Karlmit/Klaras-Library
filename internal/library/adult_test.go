package library_test

import (
	"context"
	"io"
	"testing"

	"github.com/Karlmit/Klaras-Library/internal/library"
)

// TestAdultHiddenByDefault is the property the whole feature rests on.
//
// A flagged book must be invisible to any query that has not explicitly asked
// for it. AdultHide is the zero value of Filter.Adult precisely so that a
// caller who never considered the question cannot be the one that leaks -- and
// OPDS, which builds a bare library.Filter{}, is exactly such a caller.
func TestAdultHiddenByDefault(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	all, err := s.ListBooks(ctx, library.Filter{Limit: 1, WithTotal: true})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total == nil {
		t.Fatal("no total returned")
	}
	before := *all.Total

	ids, _, err := s.BookIDs(ctx, library.Filter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) < 3 {
		t.Fatalf("need at least 3 seeded books, got %d", len(ids))
	}
	flag := ids[:3]
	if _, err := s.SetAdultMany(ctx, flag, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.SetAdultMany(context.Background(), flag, false) })

	cases := []struct {
		name string
		f    library.Filter
		want int64
	}{
		{"zero value hides", library.Filter{Limit: 1, WithTotal: true}, before - 3},
		{"explicit hide", library.Filter{Limit: 1, WithTotal: true, Adult: library.AdultHide}, before - 3},
		{"only", library.Filter{Limit: 1, WithTotal: true, Adult: library.AdultOnly}, 3},
		{"include", library.Filter{Limit: 1, WithTotal: true, Adult: library.AdultInclude}, before},
	}
	for _, c := range cases {
		got, err := s.ListBooks(ctx, c.f)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Total == nil || *got.Total != c.want {
			t.Errorf("%s: total = %v, want %d", c.name, got.Total, c.want)
		}
	}

	// BookIDs feeds "select all", so it must agree with the grid. A select-all
	// that returned hidden ids would let a bulk edit reach them.
	hidden, _, err := s.BookIDs(ctx, library.Filter{}, 100000)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range hidden {
		for _, f := range flag {
			if id == f {
				t.Fatalf("book %d is flagged but select-all returned it", id)
			}
		}
	}
}

// TestScanAdultLeavesClearedBooksAlone: clearing the flag is a human decision,
// and a later scan must not silently undo it. Without this the review is
// pointless -- every correction would be reverted by the next run.
func TestScanAdultLeavesClearedBooksAlone(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	ids, _, err := s.BookIDs(ctx, library.Filter{}, 1)
	if err != nil || len(ids) == 0 {
		t.Fatalf("no seeded books: %v", err)
	}
	id := ids[0]

	// Make it look like erotica to the scanner. adult_reason is cleared too:
	// another test in this package may have left a "cleared:" marker on this
	// row, which the scan deliberately honours.
	if _, err := s.Pool().Exec(ctx,
		`UPDATE books SET description='En erotisk novell om sommaren.',
		        adult=false, adult_reason=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(context.Background(),
			`UPDATE books SET description=NULL, adult=false, adult_reason=NULL WHERE id=$1`, id)
	})

	rep, err := s.ScanAdult(ctx, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Flagged < 1 {
		t.Fatalf("scan flagged %d books, want at least 1", rep.Flagged)
	}

	// An administrator says no.
	if err := s.SetAdult(ctx, id, false); err != nil {
		t.Fatal(err)
	}

	again, err := s.ScanAdult(ctx, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if again.Candidates != 0 {
		t.Errorf("re-scan found %d candidates, want 0: a cleared book must stay cleared",
			again.Candidates)
	}
	var adult bool
	if err := s.Pool().QueryRow(ctx, `SELECT adult FROM books WHERE id=$1`, id).Scan(&adult); err != nil {
		t.Fatal(err)
	}
	if adult {
		t.Error("the scan re-flagged a book an administrator had cleared")
	}
}

// TestFlaggingDoesNotTouchUpdatedAt: updated_at drives Kobo sync. Flagging
// 1,900 books would otherwise tell every paired device that 1,900 books had
// changed, for a field no device can see.
func TestFlaggingDoesNotTouchUpdatedAt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	ids, _, err := s.BookIDs(ctx, library.Filter{}, 1)
	if err != nil || len(ids) == 0 {
		t.Fatalf("no seeded books: %v", err)
	}
	id := ids[0]

	var before, after string
	if err := s.Pool().QueryRow(ctx,
		`SELECT updated_at::text FROM books WHERE id=$1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAdultMany(ctx, []int64{id}, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.SetAdultMany(context.Background(), []int64{id}, false) })

	if err := s.Pool().QueryRow(ctx,
		`SELECT updated_at::text FROM books WHERE id=$1`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("updated_at moved from %s to %s; every Kobo would resync", before, after)
	}
}

// TestFacetsNoticeHiddenBooks guards an interaction between two deliberate
// decisions that cancelled each other out.
//
// Flagging must not touch updated_at, because that drives Kobo sync. The facet
// refresher decided whether it had work by watching max(updated_at) and
// count(*). Flagging 1,860 books therefore changed neither signal, the counts
// were never rebuilt, and the sidebar went on reporting the old total while the
// Adult content entry stayed hidden -- because its count was still zero. The
// feature looked like it had not run.
func TestFacetsNoticeHiddenBooks(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.RefreshFacets(ctx, true); err != nil {
		t.Fatal(err)
	}
	before, err := s.Facets(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}

	ids, _, err := s.BookIDs(ctx, library.Filter{}, 4)
	if err != nil || len(ids) < 4 {
		t.Fatalf("need 4 seeded books: %v", err)
	}
	if _, err := s.SetAdultMany(ctx, ids, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.SetAdultMany(context.Background(), ids, false)
		_, _ = s.RefreshFacets(context.Background(), true)
	})

	// Not forced: this is exactly what the background refresher does.
	did, err := s.RefreshFacets(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Fatal("the refresher saw no work to do after 4 books were hidden")
	}

	after, err := s.Facets(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if after.TotalBooks != before.TotalBooks-4 {
		t.Errorf("total_books = %d, want %d", after.TotalBooks, before.TotalBooks-4)
	}
	if after.Adult != 4 {
		t.Errorf("adult count = %d, want 4; the sidebar entry is hidden while this is zero",
			after.Adult)
	}
}
