package library_test

import (
	"archive/zip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Karlmit/Klaras-Library/internal/library"
	"github.com/Karlmit/Klaras-Library/internal/provider"
)

// makeEPUB writes a minimal EPUB whose content pages are exactly what is given.
func makeEPUB(t *testing.T, path string, pages map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z := zip.NewWriter(f)
	for name, body := range pages {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
}

func seedBookWithFile(t *testing.T, s *library.Store, root, title, dir, file string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO books (uuid, title, path, description)
		VALUES (gen_random_uuid(), $1, $2, NULL) RETURNING id`, title, dir).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO book_files (book_id, format, filename, size_bytes)
		VALUES ($1,'EPUB',$2,1)`, id, file); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(context.Background(), `DELETE FROM books WHERE id=$1`, id)
	})
	return id
}

func descOf(t *testing.T, s *library.Store, id int64) string {
	t.Helper()
	var d *string
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT description FROM books WHERE id=$1`, id).Scan(&d); err != nil {
		t.Fatal(err)
	}
	if d == nil {
		return ""
	}
	return *d
}

const blurb = "Den unga änkan Cora Seaborne trotsar omgivningens förväntningar när hon " +
	"lämnar London för landsbygden i Essex, där rykten om ett mytiskt odjur skapar skräck " +
	"bland traktens invånare och sätter hennes vänskap på prov."

// TestFillFromFilesReadsThePublishersOwnBlurb covers the three shapes seen in
// this library, and the two that must be refused.
func TestFillFromFilesReadsThePublishersOwnBlurb(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	root := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cases := []struct {
		name  string
		pages map[string]string
		want  bool
	}{
		{"a file that says what it is", map[string]string{
			"OEBPS/bookinfo.html": "<html><body><h1>Om boken</h1><p>" + blurb + "</p></body></html>",
			"OEBPS/ch1.html":      "<p>Kapitel 1</p>",
		}, true},
		{"Storytel's skip-the-intro marker", map[string]string{
			"OEBPS/ch1.html": "<html><body><a>HOPPA ÖVER INTROTEXT</a><p>" + blurb + "</p></body></html>",
		}, true},
		{"an Om boken heading on an ordinary page", map[string]string{
			"OEBPS/a.html": "<html><body><h2>Om boken</h2><p>" + blurb + "</p></body></html>",
		}, true},
		{"a colophon is not a blurb", map[string]string{
			"OEBPS/bookinfo.html": "<p>Copyright © 2021 Bokförlaget Polaris. ISBN 9789177955788. " +
				"Omslag: Emma Graves. Tryckt i Litauen. Alla rättigheter förbehållna vilket " +
				"innebär att ingen del får kopieras utan tillstånd.</p>",
		}, false},
		{"an author biography is not a blurb", map[string]string{
			"OEBPS/about_book.html": "<p>Helene Flood föddes 1982 i Oslo och är psykolog. Hon " +
				"debuterade som författare 2019 och har sedan dess skrivit flera romaner som " +
				"översatts till en rad språk världen över.</p>",
		}, false},
		{"nothing to find", map[string]string{
			"OEBPS/ch1.html": "<p>Kapitel 1. Det var en gång.</p>",
		}, false},
	}

	for i, c := range cases {
		dir := filepath.Join("Author", c.name)
		file := "book.epub"
		makeEPUB(t, filepath.Join(root, dir, file), c.pages)
		id := seedBookWithFile(t, s, root, "Ormen i Essex", dir, file)

		rep, err := s.FillFromFiles(ctx, root, 1, false, log)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := descOf(t, s, id)
		if c.want && got == "" {
			t.Errorf("%s: found nothing; report %+v", c.name, rep)
		}
		if !c.want && got != "" {
			t.Errorf("%s: wrote %q, which is not a blurb", c.name, got[:min(70, len(got))])
		}
		if c.want && got != "" {
			if len(got) < 80 {
				t.Errorf("%s: kept only %q", c.name, got)
			}
			// The heading and the marker are page furniture, not prose.
			for _, junk := range []string{"Om boken", "HOPPA ÖVER"} {
				if len(got) > 0 && got[:min(len(junk), len(got))] == junk {
					t.Errorf("%s: kept the page furniture: %q", c.name, got[:40])
				}
			}
		}
		_ = i
	}
}

// TestFillFromFilesRemembersWhatItTried: the run is resumable, and a book that
// yielded nothing must not be reopened tomorrow -- that is what makes a daily
// job reach the end of the library instead of retrying its first page.
func TestFillFromFilesRemembersWhatItTried(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	root := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dir, file := filepath.Join("Author", "Empty"), "book.epub"
	makeEPUB(t, filepath.Join(root, dir, file), map[string]string{
		"OEBPS/ch1.html": "<p>Kapitel 1.</p>",
	})
	id := seedBookWithFile(t, s, root, "Utan text", dir, file)

	if _, err := s.FillFromFiles(ctx, root, 500, false, log); err != nil {
		t.Fatal(err)
	}
	var tried bool
	if err := s.Pool().QueryRow(ctx,
		`SELECT found FROM description_lookups WHERE book_id=$1 AND source='epub'`,
		id).Scan(&tried); err != nil {
		t.Fatalf("the attempt was not recorded: %v", err)
	}
	if tried {
		t.Error("recorded as found when nothing was found")
	}

	// A second run must not consider it again.
	rep, err := s.FillFromFiles(ctx, root, 500, false, log)
	if err != nil {
		t.Fatal(err)
	}
	for _, seen := range []int64{id} {
		var n int
		if err := s.Pool().QueryRow(ctx, `
			SELECT count(*) FROM description_lookups WHERE book_id=$1`, seen).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("book %d has %d lookup rows, want 1", seen, n)
		}
	}
	_ = rep
}

// stubProvider stands in for Google so the failure modes can be exercised
// without depending on the real service being in a particular mood.
type stubProvider struct {
	err  error
	desc string
	seen int
}

func (s *stubProvider) Name() string { return "stub" }
func (s *stubProvider) Search(ctx context.Context, q provider.Query, limit int) ([]provider.Result, error) {
	s.seen++
	if s.err != nil {
		return nil, s.err
	}
	return []provider.Result{{Source: "stub", Title: "Ormen i Essex", Description: blurb}}, nil
}

// TestUnavailableIsNotRecordedAsNoDescription is the guard for the failure that
// would have quietly ruined this feature.
//
// Google answers overload with 503, not 429. Treated as an ordinary error, the
// book gets written down as "asked, nothing found" -- and that verdict is
// permanent, because the whole point of the lookups table is never to ask
// twice. A bad half hour would have written off thousands of books for good.
func TestUnavailableIsNotRecordedAsNoDescription(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	id := seedBookWithISBN(t, s, "Ormen i Essex", "9789177955788")

	down := provider.NewSetOf(&stubProvider{err: provider.ErrUnavailable})
	rep, err := s.FillFromGoogle(ctx, down, 10, false, log)
	if err != nil {
		t.Fatal(err)
	}
	if rep.FromGoogle != 0 {
		t.Errorf("wrote %d descriptions while the provider was down", rep.FromGoogle)
	}
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM description_lookups WHERE book_id=$1 AND source='google'`,
		id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("recorded the book as tried when the provider never answered; " +
			"it would never be asked again")
	}

	// When the service recovers, the same book is still in the queue.
	up := provider.NewSetOf(&stubProvider{desc: blurb})
	rep, err = s.FillFromGoogle(ctx, up, 10, false, log)
	if err != nil {
		t.Fatal(err)
	}
	if rep.FromGoogle != 1 {
		t.Fatalf("after recovery wrote %d, want 1", rep.FromGoogle)
	}
	if got := descOf(t, s, id); len(got) < 80 {
		t.Errorf("description not written: %q", got)
	}
}

// TestQuotaStopsTheRunWithoutRecording: same principle, different refusal.
func TestQuotaStopsTheRunWithoutRecording(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	id := seedBookWithISBN(t, s, "Ormen i Essex", "9789177955789")

	set := provider.NewSetOf(&stubProvider{err: provider.ErrQuota})
	rep, err := s.FillFromGoogle(ctx, set, 10, false, log)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.QuotaHit {
		t.Error("quota refusal was not reported")
	}
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM description_lookups WHERE book_id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("recorded a book the provider refused to answer about")
	}
}

func seedBookWithISBN(t *testing.T, s *library.Store, title, isbn string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO books (uuid, title, path, description)
		VALUES (gen_random_uuid(), $1, 'x/'||$2, NULL) RETURNING id`, title, isbn).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO identifiers (book_id, scheme, value) VALUES ($1,'isbn',$2)`, id, isbn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.Pool().Exec(context.Background(), `DELETE FROM books WHERE id=$1`, id) })
	return id
}
