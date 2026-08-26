package library

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
)

// eroticaImprints are publishers whose entire catalogue is erotica.
//
// The publisher is stronger evidence than anything in a title. Lusthuset
// publishes "Sexklubben" and "Ner på knä" without the word erotisk appearing
// anywhere in the metadata, so a keyword scan finds 1% of its list; the imprint
// finds all of it. Conversely Hoi Förlag is deliberately absent: it is a
// general self-publishing house whose few erotic titles the keyword rules
// catch on their own, and flagging its crime novels would be worse than missing
// nothing.
//
// Matched case-insensitively against the exact publisher name. Substring
// matching would sweep up "Lusthusets trädgårdsbok".
var eroticaImprints = []string{
	"LUST",
	"CUPIDO",
	"Lusthuset",
	"Lusthuset Förlag",
	"Yourotica Förlag",
	"Förlag NITO",
}

// eroticaWords is the keyword rule, applied to titles, descriptions, series and
// tags.
//
// Deliberately narrow. "Sex" alone would match sexdagarskriget, sextiotalet and
// every book about sex education; the Swedish stems for erotic are specific
// enough to be used unqualified, and everything else here is unambiguous.
const eroticaWords = `\m(erotisk\w*|erotik|erotica|erotiken)\M`

// eroticaPhrase is the same word next to a word for the kind of book it is:
// "erotisk novell", "erotiska noveller", "den erotiska trilogin".
//
// A blurb for erotica almost always names the form. A blurb for a literary
// novel that happens to use the word usually does not -- Sigrid Undset's
// Kristin Lavransdotter and Bergman's Gycklarnas afton both match the bare
// word and neither is erotica.
//
// This does not replace the bare word, because on its own it also drops
// "Gullivers resa till sexland" and one of the Junis våta drömmar novellas.
// It grades instead: a bare mention is real evidence, just the weakest kind,
// and saying so is what lets a reviewer spend their attention where the
// mistakes are.
const eroticaPhrase = `erotisk[a-zäöå]* ` +
	`(novell|roman|berättels|serie|trilogi|samling|kalender|thriller|romance|äventyr|dagbok|historia|fantasi)` +
	`|erotica|novellsamling`

// AdultCandidate is a book the scan believes is erotica.
type AdultCandidate struct {
	ID     int64
	Title  string
	Reason string
}

// AdultReport summarises a scan.
type AdultReport struct {
	Candidates int
	ByReason   map[string]int
	Flagged    int
	Skipped    int // already flagged, or already cleared by a human
}

// ScanAdult finds erotica and flags it.
//
// Books an administrator has already reviewed are left alone: clearing the flag
// is a decision, and a later scan must not silently undo it. That is what
// adult_reason records -- a cleared book keeps its reason with a "cleared:"
// prefix, so re-running is safe and repeatable.
func (s *Store) ScanAdult(ctx context.Context, dryRun bool, out io.Writer) (*AdultReport, error) {
	rep := &AdultReport{ByReason: map[string]int{}}

	rows, err := s.pool.Query(ctx, `
		SELECT b.id, b.title,
		  CASE
		    WHEN p.name ILIKE ANY($1) THEN 'imprint: ' || p.name
		    WHEN b.title       ~* $2  THEN 'title'
		    WHEN COALESCE(b.description,'')                  ~* $3 THEN 'description'
		    WHEN COALESCE(b.description,'')                  ~* $2 THEN 'description (mention only)'
		    WHEN COALESCE(b.series_name,'')                  ~* $2 THEN 'series'
		    WHEN COALESCE(array_to_string(b.tag_names,' '),'') ~* $2 THEN 'tag'
		  END AS reason
		FROM books b
		LEFT JOIN publishers p ON p.id = b.publisher_id
		WHERE NOT b.adult
		  AND (b.adult_reason IS NULL OR b.adult_reason NOT LIKE 'cleared:%')
		  AND (
		        p.name ILIKE ANY($1)
		     OR b.title ~* $2
		     OR COALESCE(b.description,'') ~* $2
		     OR COALESCE(b.series_name,'') ~* $2
		     OR COALESCE(array_to_string(b.tag_names,' '),'') ~* $2
		  )
		ORDER BY b.title_sort, b.id`, eroticaImprints, eroticaWords, eroticaPhrase)
	if err != nil {
		return nil, fmt.Errorf("scan for adult content: %w", err)
	}
	defer rows.Close()

	var ids []int64
	var reasons []string
	for rows.Next() {
		var c AdultCandidate
		if err := rows.Scan(&c.ID, &c.Title, &c.Reason); err != nil {
			return nil, err
		}
		rep.Candidates++
		rep.ByReason[c.Reason]++
		ids = append(ids, c.ID)
		reasons = append(reasons, c.Reason)
		if out != nil {
			fmt.Fprintf(out, "%-7d %-28s %s\n", c.ID, c.Reason, c.Title)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if dryRun || len(ids) == 0 {
		return rep, nil
	}

	n, err := s.writePreservingTimestamps(ctx, `
		UPDATE books b
		SET adult = true, adult_reason = v.reason
		FROM (SELECT unnest($1::bigint[]) AS id, unnest($2::text[]) AS reason) v
		WHERE b.id = v.id`, ids, reasons)
	if err != nil {
		return nil, fmt.Errorf("flag adult content: %w", err)
	}
	rep.Flagged = int(n)
	return rep, nil
}

// SetAdult flags or clears one book.
//
// Clearing records that a human decided, so a later scan does not put the flag
// straight back.
func (s *Store) SetAdult(ctx context.Context, id int64, adult bool) error {
	var reason any
	if adult {
		reason = "set by an administrator"
	} else {
		reason = "cleared: reviewed by an administrator"
	}
	_, err := s.writePreservingTimestamps(ctx,
		`UPDATE books SET adult=$2, adult_reason=$3 WHERE id=$1`, id, adult, reason)
	return err
}

// SetAdultMany flags or clears a batch, for the review screen's bulk actions.
func (s *Store) SetAdultMany(ctx context.Context, ids []int64, adult bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	reason := "set by an administrator"
	if !adult {
		reason = "cleared: reviewed by an administrator"
	}
	return s.writePreservingTimestamps(ctx,
		`UPDATE books SET adult=$2, adult_reason=$3 WHERE id = ANY($1)`, ids, adult, reason)
}

// writePreservingTimestamps runs an update that must not count as an edit.
//
// updated_at drives Kobo sync, and the books trigger stamps it on any update
// that leaves it unchanged -- writing updated_at = updated_at is exactly the
// auto-touch condition, so there is no way to preserve it from the statement
// itself. The transaction-local setting tells the trigger to leave it alone;
// SET LOCAL means it cannot outlive this transaction or follow the connection
// back into the pool.
func (s *Store) writePreservingTimestamps(ctx context.Context, sql string, args ...any) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `SET LOCAL klaras.preserve_updated_at = 'on'`); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SortedReasons returns the report's reasons, commonest first, for printing.
func (r *AdultReport) SortedReasons() []string {
	out := make([]string, 0, len(r.ByReason))
	for k := range r.ByReason {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if r.ByReason[out[i]] != r.ByReason[out[j]] {
			return r.ByReason[out[i]] > r.ByReason[out[j]]
		}
		return strings.Compare(out[i], out[j]) < 0
	})
	return out
}
