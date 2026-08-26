package library_test

import (
	"context"
	"testing"

	"github.com/Karlmit/Klaras-Library/internal/library"
)

// TestDiscoveryShelfIsCreatedOnce: the shelf is looked up by a flag, not by
// name, so a reader is free to rename it without the feature losing track.
func TestDiscoveryShelfIsCreatedOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	uid := seedUser(t, s, "discoverer")

	a, err := s.DiscoveryShelf(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx,
		`UPDATE shelves SET name='Someday' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	b, err := s.DiscoveryShelf(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("renaming the shelf produced a second one: %d then %d", a, b)
	}
}

// TestDiscoverDeckSkipsWhatIsDecided is the property that makes the screen
// finite: a book that has been kept or passed must not come round again.
func TestDiscoverDeckSkipsWhatIsDecided(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	uid := seedUser(t, s, "swiper")

	first, err := s.DiscoverDeck(ctx, uid, 5, library.AdultHide)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 3 {
		t.Fatalf("deck returned %d cards, need at least 3 to test", len(first))
	}
	kept, passed := first[0].ID, first[1].ID
	if err := s.DiscoverKeep(ctx, uid, kept); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscoverPass(ctx, uid, passed); err != nil {
		t.Fatal(err)
	}

	// Ask for the whole library; neither decided book may appear.
	all, err := s.DiscoverDeck(ctx, uid, 40, library.AdultHide)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range all {
		if c.ID == kept {
			t.Error("a kept book was offered again")
		}
		if c.ID == passed {
			t.Error("a passed book was offered again")
		}
	}

	st, err := s.DiscoverStatsFor(ctx, uid, library.AdultHide)
	if err != nil {
		t.Fatal(err)
	}
	if st.Kept != 1 || st.Passed != 1 {
		t.Errorf("stats = kept %d, passed %d; want 1 and 1", st.Kept, st.Passed)
	}

	// Undo puts it back in circulation, both ways.
	if err := s.DiscoverUndo(ctx, uid, passed); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscoverUndo(ctx, uid, kept); err != nil {
		t.Fatal(err)
	}
	st, err = s.DiscoverStatsFor(ctx, uid, library.AdultHide)
	if err != nil {
		t.Fatal(err)
	}
	if st.Kept != 0 || st.Passed != 0 {
		t.Errorf("after undo stats = kept %d, passed %d; want 0 and 0", st.Kept, st.Passed)
	}
}

// TestDiscoverNeverOffersAdultBooks: the screen shows whatever it is given
// without a reader asking for it, so it must not be the place the adult filter
// is forgotten.
func TestDiscoverNeverOffersAdultBooks(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	uid := seedUser(t, s, "browser")

	ids, _, err := s.BookIDs(ctx, library.Filter{}, 6)
	if err != nil || len(ids) < 6 {
		t.Fatalf("need seeded books: %v", err)
	}
	if _, err := s.SetAdultMany(ctx, ids, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.SetAdultMany(context.Background(), ids, false) })

	deck, err := s.DiscoverDeck(ctx, uid, 40, library.AdultHide)
	if err != nil {
		t.Fatal(err)
	}
	flagged := map[int64]bool{}
	for _, id := range ids {
		flagged[id] = true
	}
	for _, c := range deck {
		if flagged[c.ID] {
			t.Fatalf("book %d is flagged adult and was offered on the discovery screen", c.ID)
		}
	}
}

// TestKeepUpdatesShelfTimestamp: the shelf's updated_at drives Kobo collection
// sync, so a book kept on a phone has to make the device notice.
func TestKeepUpdatesShelfTimestamp(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	uid := seedUser(t, s, "syncer")
	shelf, err := s.DiscoveryShelf(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx,
		`UPDATE shelves SET updated_at = now() - interval '1 day' WHERE id=$1`, shelf); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := s.Pool().QueryRow(ctx,
		`SELECT updated_at::text FROM shelves WHERE id=$1`, shelf).Scan(&before); err != nil {
		t.Fatal(err)
	}
	deck, err := s.DiscoverDeck(ctx, uid, 1, library.AdultHide)
	if err != nil || len(deck) == 0 {
		t.Fatalf("empty deck: %v", err)
	}
	if err := s.DiscoverKeep(ctx, uid, deck[0].ID); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := s.Pool().QueryRow(ctx,
		`SELECT updated_at::text FROM shelves WHERE id=$1`, shelf).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("keeping a book left the shelf timestamp alone; no Kobo would notice")
	}
}

func seedUser(t *testing.T, s *library.Store, name string) int64 {
	t.Helper()
	var id int64
	err := s.Pool().QueryRow(context.Background(), `
		INSERT INTO users (username, email, role, password_hash, is_active)
		VALUES ($1, $1 || '@example.test', 'reader', 'x', true)
		ON CONFLICT (lower(username)) DO UPDATE SET email = EXCLUDED.email
		RETURNING id`, name).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
