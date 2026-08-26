-- +goose Up

-- What has already been asked about, so a daily job resumes rather than
-- restarts.
--
-- Google Books allows 1,000 lookups a day on the free tier and this library has
-- over ten thousand books without a blurb, so filling them takes a week and a
-- half of short runs. Without a record of what has been tried, every run would
-- re-ask the same first thousand and never reach the rest.
--
-- Failures are recorded as deliberately as successes: roughly half of all
-- lookups return nothing, and re-asking those tomorrow would waste the quota
-- that the untried books need.
CREATE TABLE description_lookups (
    book_id  bigint      NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    source   text        NOT NULL,
    tried_at timestamptz NOT NULL DEFAULT now(),
    found    boolean     NOT NULL,
    PRIMARY KEY (book_id, source)
);

CREATE INDEX description_lookups_tried_idx ON description_lookups (source, tried_at);

-- +goose Down
DROP TABLE IF EXISTS description_lookups;
