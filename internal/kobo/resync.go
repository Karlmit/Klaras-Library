package kobo

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ForgetSynced discards the record of what a user's devices have been told, so
// the next sync announces every book on their Kobo shelves as new.
//
// This is calibre-web's "force full Kobo sync". It is the remedy when the
// record and the device disagree -- a device restored from backup or factory
// reset, a book that was announced into a response that never arrived, or a
// sync state corrupted by requests made on the device's behalf. Forgetting is
// always the safe direction: a book announced as new that the device already
// holds costs one redundant download, while a book wrongly believed delivered
// can never arrive at all.
//
// Clearing the record is also what lets the next sync disregard the device's
// stored watermark; see handleSync. Both halves are needed, which is why this
// lives in one place rather than being written out twice.
func ForgetSynced(ctx context.Context, pool *pgxpool.Pool, userID int64) (int64, error) {
	tag, err := pool.Exec(ctx,
		`DELETE FROM kobo_synced_books WHERE user_id = $1`, userID)
	if err != nil {
		return 0, err
	}
	// Touch the synced shelves so the collections are re-sent too, not just the
	// books in them.
	if _, err := pool.Exec(ctx,
		`UPDATE shelves SET updated_at = now() WHERE user_id = $1 AND kobo_sync`, userID); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
