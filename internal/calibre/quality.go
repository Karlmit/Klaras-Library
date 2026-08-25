package calibre

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// QualityIssue is one category of suspect data found after import.
type QualityIssue struct {
	Reason string
	Count  int64
	Note   string
}

// qualityChecks flag imported data that looks wrong but that we must not
// "fix" automatically -- splitting a merged author name or discarding a date
// is a judgement call for a human, and guessing silently is worse than
// importing faithfully and saying so.
//
// Each entry sets one reason on the matching books. They are additive: a book
// can collect several reasons.
var qualityChecks = []struct {
	reason string
	note   string
	where  string
}{
	{
		reason: "author_name_has_separator",
		note:   "author contains ; or | -- probably two authors merged, or a sort name stored as a display name",
		where: `EXISTS (SELECT 1 FROM book_authors ba JOIN authors a ON a.id = ba.author_id
		         WHERE ba.book_id = b.id AND a.name ~ '[;|]')`,
	},
	{
		reason: "implausible_pubdate",
		note:   "publication date before 1450 or more than 2 years in the future",
		where:  `b.pubdate IS NOT NULL AND (b.pubdate < DATE '1450-01-01' OR b.pubdate > (now() + interval '2 years')::date)`,
	},
	{
		reason: "no_files",
		note:   "no file recorded on disk for this book",
		where:  `NOT EXISTS (SELECT 1 FROM book_files f WHERE f.book_id = b.id)`,
	},
	{
		reason: "no_epub",
		note:   "no EPUB; cannot be converted to KEPUB for Kobo",
		where: `NOT EXISTS (SELECT 1 FROM book_files f WHERE f.book_id = b.id AND f.format = 'EPUB')
		        AND EXISTS (SELECT 1 FROM book_files f WHERE f.book_id = b.id)`,
	},
	{
		reason: "duplicate_title_author",
		note:   "another book shares this exact title and author list",
		where: `EXISTS (SELECT 1 FROM books o
		         WHERE o.id <> b.id
		           AND lower(o.title) = lower(b.title)
		           AND o.author_names = b.author_names)`,
	},
	{
		reason: "html_entities_in_title",
		note:   "title contains raw HTML entities such as &quot; -- it will display literally",
		where:  `b.title ~ '&(quot|amp|lt|gt|apos|#[0-9]+);'`,
	},
	{
		reason: "empty_title",
		note:   "title is missing or a placeholder",
		where:  `btrim(b.title) = '' OR b.title = 'Unknown'`,
	},
}

// flagQualityIssues marks suspect books and returns what it found.
//
// This bumps updated_at as a side effect of the UPDATE, which is why the
// importer runs it BEFORE restoring Calibre's last_modified. Attempting to
// preserve the timestamp here by assigning updated_at = b.updated_at does not
// work: the books trigger treats an unchanged value as "caller did not set it"
// and auto-touches anyway.
func flagQualityIssues(ctx context.Context, tx pgx.Tx) ([]QualityIssue, error) {
	out := make([]QualityIssue, 0, len(qualityChecks))
	for _, c := range qualityChecks {
		q := fmt.Sprintf(`
			UPDATE books b
			   SET needs_review  = true,
			       review_reasons = array_append(b.review_reasons, $1)
			 WHERE (%s)
			   AND NOT (b.review_reasons @> ARRAY[$1])`, c.where)
		tag, err := tx.Exec(ctx, q, c.reason)
		if err != nil {
			return nil, fmt.Errorf("check %s: %w", c.reason, err)
		}
		if n := tag.RowsAffected(); n > 0 {
			out = append(out, QualityIssue{Reason: c.reason, Count: n, Note: c.note})
		}
	}
	return out, nil
}
