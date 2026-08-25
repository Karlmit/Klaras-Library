package library_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/Karlmit/Klaras-Library/internal/devseed"
	"github.com/Karlmit/Klaras-Library/internal/library"
	"github.com/Karlmit/Klaras-Library/internal/store"
	"github.com/Karlmit/Klaras-Library/internal/testdb"
)

const seedBooks = 500

func newStore(t *testing.T) *library.Store {
	t.Helper()
	dsn := testdb.For(t, os.Getenv("KLARAS_TEST_DATABASE_URL"), "library")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	if err := store.Migrate(ctx, dsn, log); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, dsn, 8, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	var n int64
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM books`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != seedBooks {
		if _, err := devseed.Run(ctx, db.Pool, seedBooks, log); err != nil {
			t.Fatal(err)
		}
	}
	s := library.New(db.Pool)
	if _, err := s.RefreshFacets(ctx, true); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestPaginateEveryBookInEverySortOrder walks the whole library one page at a
// time, through the same code path the UI uses.
//
// This is the test that was missing. The query-plan tests hand-wrote their
// keyset SQL, so they proved the shape was index-friendly but never exercised
// ListBooks' own cursor substitution -- which corrupted the placeholder and
// made every page after the first fail with a 500. Loading page one and
// calling it working is exactly how that shipped.
func TestPaginateEveryBookInEverySortOrder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	for _, sort := range []library.SortMode{
		library.SortTitle, library.SortAuthor, library.SortAdded,
		library.SortPubDate, library.SortRating, library.SortSeries,
	} {
		t.Run(string(sort), func(t *testing.T) {
			seen := map[int64]int{}
			cursor := ""
			pages := 0

			for {
				page, err := s.ListBooks(ctx, library.Filter{
					Sort: sort, Limit: 37, Cursor: cursor,
				})
				if err != nil {
					t.Fatalf("page %d failed: %v", pages+1, err)
				}
				pages++
				for _, b := range page.Items {
					seen[b.ID]++
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
				if pages > 100 {
					t.Fatal("pagination did not terminate")
				}
			}

			if pages < 2 {
				t.Fatalf("only %d page for %d books; the cursor was never exercised",
					pages, seedBooks)
			}
			// Every book exactly once: no gaps, no repeats.
			for id, n := range seen {
				if n != 1 {
					t.Errorf("book %d appeared %d times", id, n)
				}
			}
			if len(seen) != seedBooks {
				t.Errorf("saw %d distinct books across %d pages, want %d",
					len(seen), pages, seedBooks)
			}
		})
	}
}

// TestPaginateWithFilters covers the same path with a WHERE clause in play,
// since the cursor placeholders are appended after the filter's own binds.
func TestPaginateWithFilters(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	facets, err := s.Facets(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(facets.Tags) == 0 {
		t.Skip("no tags in the fixture")
	}
	tag := facets.Tags[0].Value

	seen := map[int64]bool{}
	cursor := ""
	for pages := 0; pages < 100; pages++ {
		page, err := s.ListBooks(ctx, library.Filter{
			Tag: tag, Sort: library.SortTitle, Limit: 20, Cursor: cursor, WithTotal: true,
		})
		if err != nil {
			t.Fatalf("filtered page %d failed: %v", pages+1, err)
		}
		for _, b := range page.Items {
			if seen[b.ID] {
				t.Errorf("book %d repeated across filtered pages", b.ID)
			}
			seen[b.ID] = true
		}
		if page.NextCursor == "" {
			if page.Total != nil && int64(len(seen)) != *page.Total {
				t.Errorf("paginated %d books but total says %d", len(seen), *page.Total)
			}
			return
		}
		cursor = page.NextCursor
	}
	t.Fatal("filtered pagination did not terminate")
}

// TestSearchPaginates covers relevance ordering, which uses a different cursor.
func TestSearchPaginates(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	seen := map[int64]bool{}
	cursor := ""
	for pages := 0; pages < 30; pages++ {
		page, err := s.ListBooks(ctx, library.Filter{
			Query: "house", Sort: library.SortRelevant, Limit: 15, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("search page %d failed: %v", pages+1, err)
		}
		for _, b := range page.Items {
			if seen[b.ID] {
				t.Errorf("book %d repeated across search pages", b.ID)
			}
			seen[b.ID] = true
		}
		if page.NextCursor == "" {
			return
		}
		cursor = page.NextCursor
	}
	t.Fatal("search pagination did not terminate")
}

// TestRejectsAMismatchedCursor: a cursor from one sort must not be silently
// applied to another, which would skip or repeat books.
func TestRejectsAMismatchedCursor(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	page, err := s.ListBooks(ctx, library.Filter{Sort: library.SortTitle, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor == "" {
		t.Skip("fixture too small")
	}
	if _, err := s.ListBooks(ctx, library.Filter{
		Sort: library.SortAuthor, Limit: 5, Cursor: page.NextCursor,
	}); err == nil {
		t.Error("a title cursor was accepted for an author sort")
	}
}
