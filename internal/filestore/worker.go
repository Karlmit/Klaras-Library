package filestore

import (
	"context"
	"fmt"

	"github.com/Karlmit/Klaras-Library/internal/jobs"
)

// MovePayload identifies a book whose files should be relocated.
type MovePayload struct {
	BookID int64 `json:"book_id"`
}

// Handler processes queued file moves.
//
// Moves run through the queue rather than inline in the request for two
// reasons: a bulk edit of 500 books would otherwise block the HTTP response on
// hundreds of filesystem operations, and a move interrupted by a restart is
// retried rather than lost.
func (s *Store) Handler() jobs.Handler {
	return func(ctx context.Context, j *jobs.Job) error {
		var p MovePayload
		if err := j.Decode(&p); err != nil {
			return fmt.Errorf("%w: bad payload: %v", jobs.ErrPermanent, err)
		}
		plan, err := s.PlanFor(ctx, p.BookID)
		if err != nil {
			// A deleted book cannot be moved, and retrying will not help.
			return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
		}
		if plan.Empty() {
			return nil
		}
		return s.Apply(ctx, plan)
	}
}
