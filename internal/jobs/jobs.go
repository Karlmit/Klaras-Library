// Package jobs is a small durable work queue backed by Postgres.
//
// Background work in this application is all of one shape -- convert this book,
// generate that thumbnail, move this file -- and all of it must survive a
// restart. SELECT ... FOR UPDATE SKIP LOCKED covers that in a few dozen lines,
// so the semantics stay visible here instead of inside a dependency.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Kind names a class of work.
type Kind string

const (
	KindThumbnail Kind = "thumbnail"
	KindKepub     Kind = "kepub"
	KindFileMove  Kind = "file_move"
)

// Job is one unit of work claimed from the queue.
type Job struct {
	ID       int64
	Kind     Kind
	Dedupe   string
	Payload  json.RawMessage
	Attempts int
}

// Decode unmarshals the payload into v.
func (j *Job) Decode(v any) error { return json.Unmarshal(j.Payload, v) }

// Handler processes one job. Returning an error schedules a retry until
// max_attempts is reached; returning ErrPermanent fails it immediately.
type Handler func(ctx context.Context, j *Job) error

// ErrPermanent marks a failure that retrying cannot fix -- a missing source
// file, a corrupt EPUB. Retrying those just burns the queue.
var ErrPermanent = errors.New("permanent failure")

// Queue enqueues and runs jobs.
type Queue struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	id   string // identifies this process in locked_by
}

// New builds a queue.
func New(pool *pgxpool.Pool, log *slog.Logger) *Queue {
	host, _ := os.Hostname()
	return &Queue{
		pool: pool,
		log:  log,
		id:   fmt.Sprintf("%s/%d", host, os.Getpid()),
	}
}

// Enqueue adds work, ignoring it if the same (kind, key) is already outstanding.
func (q *Queue) Enqueue(ctx context.Context, kind Kind, key string, payload any, priority int) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = q.pool.Exec(ctx, `
		INSERT INTO jobs (kind, dedupe_key, payload, priority)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT DO NOTHING`, string(kind), key, b, priority)
	return err
}

// EnqueueTx adds work inside an existing transaction, so queueing and the
// change that motivated it commit together or not at all.
func (q *Queue) EnqueueTx(ctx context.Context, tx pgx.Tx, kind Kind, key string, payload any, priority int) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO jobs (kind, dedupe_key, payload, priority)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT DO NOTHING`, string(kind), key, b, priority)
	return err
}

// Stats counts jobs by state for one kind.
type Stats struct {
	Pending int64 `json:"pending"`
	Running int64 `json:"running"`
	Failed  int64 `json:"failed"`
	Done    int64 `json:"done"`
}

// Stats reports queue depth, for the admin UI and for tests.
func (q *Queue) Stats(ctx context.Context, kind Kind) (Stats, error) {
	var s Stats
	err := q.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE state='pending'),
		       count(*) FILTER (WHERE state='running'),
		       count(*) FILTER (WHERE state='failed'),
		       count(*) FILTER (WHERE state='done')
		FROM jobs WHERE kind=$1`, string(kind)).
		Scan(&s.Pending, &s.Running, &s.Failed, &s.Done)
	return s, err
}

// claim takes one runnable job, locking it against other workers.
func (q *Queue) claim(ctx context.Context, kind Kind) (*Job, error) {
	var j Job
	var kindStr string
	err := q.pool.QueryRow(ctx, `
		UPDATE jobs SET state='running', locked_at=now(), locked_by=$2, attempts=attempts+1
		WHERE id = (
			SELECT id FROM jobs
			WHERE kind=$1 AND state='pending' AND run_after <= now()
			ORDER BY priority, run_after, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, kind, dedupe_key, payload, attempts`, string(kind), q.id).
		Scan(&j.ID, &kindStr, &j.Dedupe, &j.Payload, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.Kind = Kind(kindStr)
	return &j, nil
}

func (q *Queue) succeed(ctx context.Context, id int64) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs SET state='done', finished_at=now(), locked_at=NULL, locked_by=NULL
		WHERE id=$1`, id)
	return err
}

func (q *Queue) fail(ctx context.Context, j *Job, cause error, permanent bool) error {
	if permanent || j.Attempts >= 5 {
		_, err := q.pool.Exec(ctx, `
			UPDATE jobs SET state='failed', last_error=$2, finished_at=now(),
			                locked_at=NULL, locked_by=NULL
			WHERE id=$1`, j.ID, cause.Error())
		return err
	}
	// Exponential backoff, capped: 2s, 8s, 32s, 128s...
	backoff := time.Duration(1<<(2*j.Attempts)) * time.Second
	if backoff > 10*time.Minute {
		backoff = 10 * time.Minute
	}
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs SET state='pending', last_error=$2, run_after=now()+$3::interval,
		                locked_at=NULL, locked_by=NULL
		WHERE id=$1`, j.ID, cause.Error(), backoff.String())
	return err
}

// ReclaimStuck returns jobs abandoned by a crashed worker to the pending pool.
// Called at startup: a 'running' row with no live worker would otherwise sit
// there forever, and its dedupe key would block the work being re-queued.
func (q *Queue) ReclaimStuck(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := q.pool.Exec(ctx, `
		UPDATE jobs SET state='pending', locked_at=NULL, locked_by=NULL,
		                last_error='reclaimed after worker restart'
		WHERE state='running' AND locked_at < now() - $1::interval`, olderThan.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Run works the queue for one kind until ctx is cancelled, with n concurrent
// workers. It polls, because the volumes here are low and LISTEN/NOTIFY would
// add a connection and a failure mode for no practical gain.
func (q *Queue) Run(ctx context.Context, kind Kind, n int, poll time.Duration, h Handler) {
	if n < 1 {
		n = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			q.workLoop(ctx, kind, poll, h)
		}(i)
	}
	wg.Wait()
}

func (q *Queue) workLoop(ctx context.Context, kind Kind, poll time.Duration, h Handler) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		worked := false
		for {
			if ctx.Err() != nil {
				return
			}
			j, err := q.claim(ctx, kind)
			if err != nil {
				q.log.Error("job claim failed", "kind", kind, "err", err)
				break
			}
			if j == nil {
				break
			}
			worked = true
			q.runOne(ctx, j, h)
		}

		next := poll
		if worked {
			next = 50 * time.Millisecond // drain a backlog quickly
		}
		timer.Reset(next)
	}
}

func (q *Queue) runOne(ctx context.Context, j *Job, h Handler) {
	defer func() {
		// A panicking handler must not take the worker down with it.
		if r := recover(); r != nil {
			q.log.Error("job panicked", "kind", j.Kind, "id", j.ID, "panic", r)
			_ = q.fail(context.WithoutCancel(ctx), j, fmt.Errorf("panic: %v", r), true)
		}
	}()

	err := h(ctx, j)
	// Use an uncancellable context for bookkeeping so a shutdown mid-job still
	// records the outcome instead of leaving the row locked.
	bookkeeping := context.WithoutCancel(ctx)
	switch {
	case err == nil:
		if err := q.succeed(bookkeeping, j.ID); err != nil {
			q.log.Error("marking job done failed", "id", j.ID, "err", err)
		}
	case errors.Is(err, context.Canceled):
		// Shutdown, not a failure: put it straight back.
		if _, e := q.pool.Exec(bookkeeping,
			`UPDATE jobs SET state='pending', attempts=attempts-1, locked_at=NULL, locked_by=NULL
			 WHERE id=$1`, j.ID); e != nil {
			q.log.Error("requeue on shutdown failed", "id", j.ID, "err", e)
		}
	default:
		q.log.Warn("job failed", "kind", j.Kind, "id", j.ID,
			"attempt", j.Attempts, "err", err)
		if e := q.fail(bookkeeping, j, err, errors.Is(err, ErrPermanent)); e != nil {
			q.log.Error("marking job failed failed", "id", j.ID, "err", e)
		}
	}
}
