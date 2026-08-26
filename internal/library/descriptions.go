package library

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Karlmit/Klaras-Library/internal/provider"
)

// Filling in missing descriptions.
//
// Two sources, in this order, because they differ in both cost and quality.
//
// The book's own file is free, offline and authoritative: several Swedish
// publishers ship the back-cover text as a content page, named bookinfo or
// about_book, or marked with Storytel's "HOPPA ÖVER INTROTEXT". Roughly one
// book in eleven has one.
//
// Google Books covers far more but costs quota -- 1,000 lookups a day on the
// free tier -- so it runs second, only for what the files could not supply,
// and only for books with an ISBN. Matching on title and author instead would
// attach the wrong blurb to a book often enough to matter, and a wrong blurb is
// worse than none: it reads as authoritative and nobody re-checks it.

var (
	blurbFile   = regexp.MustCompile(`(?i)(bookinfo|book[-_]?info|om[-_ ]?bok|about[-_ ]?book|about[-_ ]?the[-_ ]?book|baksid|blurb|synopsis|presentation)`)
	blurbMarker = regexp.MustCompile(`(?i)HOPPA\s+ÖVER\s+INTROTEXT`)
	blurbHead   = regexp.MustCompile(`(?i)^\s*om\s+(boken|bogen|denna\s+bok)\b`)
	notBlurb    = regexp.MustCompile(`(?i)(copyright|©|all rights reserved|\bisbn\b|utgiven av|omslag:|översättning:|tryckt|www\.)`)
	authorBio   = regexp.MustCompile(`(?i)(föddes|är född|debuterade|är författare|har skrivit|prisbelönt författare)`)
	htmlTag     = regexp.MustCompile(`<[^>]+>`)
	scriptOrCSS = regexp.MustCompile(`(?is)<(script|style).*?</(script|style)>`)
	manySpaces  = regexp.MustCompile(`\s+`)
)

// DescriptionReport is what a fill run did.
type DescriptionReport struct {
	Missing    int
	FromFiles  int
	FromGoogle int
	Asked      int
	NotFound   int
	QuotaHit   bool
	// Unavailable counts consecutive 5xx refusals, reset by any real answer.
	Unavailable int
	Elapsed     time.Duration
}

type descCandidate struct {
	ID    int64
	Title string
	Path  string
	Files []string
	ISBN  string
}

// missingDescriptions lists books with no description, newest first so a
// library that is still growing fills in the books someone just added before
// the ones that have waited a year.
func (s *Store) missingDescriptions(ctx context.Context, source string, limit int) ([]descCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.id, b.title, b.path,
		       COALESCE(ARRAY(SELECT f.filename FROM book_files f
		                       WHERE f.book_id = b.id AND f.format IN ('EPUB','KEPUB')
		                       ORDER BY CASE f.format WHEN 'EPUB' THEN 0 ELSE 1 END), '{}'),
		       COALESCE((SELECT i.value FROM identifiers i
		                  WHERE i.book_id = b.id AND i.scheme = 'isbn' LIMIT 1), '')
		FROM books b
		WHERE COALESCE(btrim(b.description), '') = ''
		  AND NOT EXISTS (SELECT 1 FROM description_lookups d
		                   WHERE d.book_id = b.id AND d.source = $1)
		ORDER BY b.added_at DESC, b.id DESC
		LIMIT $2`, source, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []descCandidate
	for rows.Next() {
		var c descCandidate
		if err := rows.Scan(&c.ID, &c.Title, &c.Path, &c.Files, &c.ISBN); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) recordLookup(ctx context.Context, id int64, source string, found bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO description_lookups (book_id, source, found) VALUES ($1,$2,$3)
		ON CONFLICT (book_id, source) DO UPDATE SET tried_at = now(), found = EXCLUDED.found`,
		id, source, found)
	return err
}

// setDescription writes one without disturbing updated_at, which drives Kobo
// sync: filling thousands of blurbs must not tell every device that thousands
// of books have changed.
func (s *Store) setDescription(ctx context.Context, id int64, text string) error {
	_, err := s.writePreservingTimestamps(ctx,
		`UPDATE books SET description = $2 WHERE id = $1`, id, text)
	return err
}

// MissingDescriptionCount is for reporting.
func (s *Store) MissingDescriptionCount(ctx context.Context) (total, missing, withISBN int64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE COALESCE(btrim(description),'') = ''),
		       count(*) FILTER (WHERE COALESCE(btrim(description),'') = ''
		                          AND EXISTS (SELECT 1 FROM identifiers i
		                                       WHERE i.book_id = books.id AND i.scheme='isbn'))
		FROM books`).Scan(&total, &missing, &withISBN)
	return
}

// FillFromFiles reads blurbs out of the books' own EPUBs. Free and offline, so
// it is never capped by anything but the number of books left.
func (s *Store) FillFromFiles(ctx context.Context, root string, limit int, dryRun bool, log *slog.Logger) (*DescriptionReport, error) {
	start := time.Now()
	rep := &DescriptionReport{}
	books, err := s.missingDescriptions(ctx, "epub", limit)
	if err != nil {
		return nil, err
	}
	rep.Missing = len(books)
	for _, b := range books {
		if ctx.Err() != nil {
			break
		}
		text := blurbFromFiles(root, b)
		if !dryRun {
			if err := s.recordLookup(ctx, b.ID, "epub", text != ""); err != nil {
				log.Warn("record lookup", "book", b.ID, "err", err)
			}
		}
		if text == "" {
			rep.NotFound++
			continue
		}
		rep.FromFiles++
		if dryRun {
			continue
		}
		if err := s.setDescription(ctx, b.ID, text); err != nil {
			log.Warn("write description", "book", b.ID, "err", err)
		}
	}
	rep.Elapsed = time.Since(start)
	return rep, nil
}

// FillFromGoogle looks up what the files could not supply.
//
// Stops at the first quota refusal rather than spending the rest of the run on
// errors; tomorrow's run resumes from the same place because every attempt is
// recorded, successful or not.
func (s *Store) FillFromGoogle(ctx context.Context, set *provider.Set, limit int, dryRun bool, log *slog.Logger) (*DescriptionReport, error) {
	start := time.Now()
	rep := &DescriptionReport{}
	books, err := s.missingDescriptions(ctx, "google", limit*3)
	if err != nil {
		return nil, err
	}
	for _, b := range books {
		if ctx.Err() != nil || rep.Asked >= limit {
			break
		}
		if b.ISBN == "" {
			continue // title matching attaches the wrong blurb too often
		}
		rep.Missing++
		rep.Asked++

		results, err := set.SearchOne(ctx, provider.Query{ISBN: b.ISBN}, 3)

		// A refusal is not an answer. Quota and unavailability both leave the
		// book untried, so tomorrow's run asks again; only a provider that
		// actually replied gets to say a book has no description, because that
		// verdict is written down and never revisited.
		if errors.Is(err, provider.ErrQuota) {
			rep.QuotaHit = true
			rep.Asked--
			log.Info("google books daily quota reached; stopping until tomorrow")
			break
		}
		if errors.Is(err, provider.ErrUnavailable) {
			rep.Asked--
			rep.Unavailable++
			// Google answers overload with 503 for minutes at a time. Pushing
			// on means a long run of nothing; stopping costs one night.
			if rep.Unavailable >= 5 {
				log.Info("google books is refusing service; stopping until the next run",
					"consecutive_5xx", rep.Unavailable)
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
			continue
		}
		rep.Unavailable = 0

		text := ""
		if err == nil {
			text = pickDescription(results, b.Title)
		}
		if !dryRun {
			if e := s.recordLookup(ctx, b.ID, "google", text != ""); e != nil {
				log.Warn("record lookup", "book", b.ID, "err", e)
			}
		}
		if text == "" {
			rep.NotFound++
		} else {
			rep.FromGoogle++
			if !dryRun {
				if e := s.setDescription(ctx, b.ID, text); e != nil {
					log.Warn("write description", "book", b.ID, "err", e)
				}
			}
		}
		// Paced well under any burst limit; the cap is the day's quota, not
		// the rate.
		select {
		case <-ctx.Done():
		case <-time.After(350 * time.Millisecond):
		}
	}
	rep.Elapsed = time.Since(start)
	return rep, nil
}

// pickDescription takes a result's description only when the result is
// plausibly the same book.
func pickDescription(rs []provider.Result, ourTitle string) string {
	for _, r := range rs {
		d := strings.TrimSpace(manySpaces.ReplaceAllString(
			html.UnescapeString(htmlTag.ReplaceAllString(r.Description, " ")), " "))
		if len(d) < 80 || len(d) > 4000 {
			continue
		}
		if titleSimilarity(r.Title, ourTitle) < 0.5 {
			continue // an ISBN is only as good as the metadata it came from
		}
		if titleSimilarity(firstN(d, 80), ourTitle) > 0.8 {
			continue // the "description" is the title again
		}
		return d
	}
	return ""
}

// blurbFromFiles digs the publisher's own text out of an EPUB.
func blurbFromFiles(root string, b descCandidate) string {
	for _, name := range b.Files {
		p := filepath.Join(root, b.Path, name)
		zr, err := zip.OpenReader(p)
		if err != nil {
			continue
		}
		var docs []*zip.File
		for _, f := range zr.File {
			l := strings.ToLower(f.Name)
			if strings.HasSuffix(l, ".html") || strings.HasSuffix(l, ".xhtml") || strings.HasSuffix(l, ".htm") {
				docs = append(docs, f)
			}
		}
		sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })

		got := ""
		for _, f := range docs { // a file that says what it is
			if blurbFile.MatchString(filepath.Base(f.Name)) {
				if t := cleanBlurb(readZipText(f), b.Title); usableBlurb(t) {
					got = t
					break
				}
			}
		}
		if got == "" { // Storytel's skip-the-intro marker sits right before it
			for i, f := range docs {
				if i >= 10 {
					break
				}
				raw := readZipText(f)
				if blurbMarker.MatchString(raw) {
					if t := cleanBlurb(raw, b.Title); usableBlurb(t) {
						got = t
						break
					}
				}
			}
		}
		if got == "" {
			for i, f := range docs {
				if i >= 8 {
					break
				}
				raw := readZipText(f)
				stripped := strings.TrimSpace(strings.TrimPrefix(raw, b.Title))
				if blurbHead.MatchString(stripped) {
					if t := cleanBlurb(raw, b.Title); usableBlurb(t) {
						got = t
						break
					}
				}
			}
		}
		zr.Close()
		if got != "" {
			return got
		}
	}
	return ""
}

func readZipText(f *zip.File) string {
	rc, err := f.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return ""
	}
	t := scriptOrCSS.ReplaceAllString(string(raw), " ")
	t = html.UnescapeString(htmlTag.ReplaceAllString(t, " "))
	return strings.TrimSpace(manySpaces.ReplaceAllString(t, " "))
}

func cleanBlurb(t, title string) string {
	if m := blurbMarker.FindStringIndex(t); m != nil {
		t = t[m[1]:]
	}
	head := regexp.MustCompile(`(?i)\bom\s+(?:boken|bogen|` + regexp.QuoteMeta(title) + `)\b\s*[:.\-–]?\s*`)
	if m := head.FindStringIndex(t); m != nil && m[0] < 120 {
		t = t[m[1]:]
	}
	t = strings.TrimPrefix(t, title)
	t = strings.Trim(manySpaces.ReplaceAllString(t, " "), " :.-–—")
	// A short leading fragment with no sentence end is a running head.
	if i := strings.Index(t, ". "); i > 0 && i < 30 && len(t) > 120 {
		t = strings.Trim(t[i+1:], " :.-–—")
	}
	return strings.TrimSpace(t)
}

func usableBlurb(t string) bool {
	if len(t) < 80 || len(t) > 3000 {
		return false
	}
	head := firstN(t, 130)
	return !notBlurb.MatchString(head) && !authorBio.MatchString(head)
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// titleSimilarity compares titles loosely: case, accents and punctuation vary
// between our metadata and a catalogue's, and none of that means a different
// book.
func titleSimilarity(a, b string) float64 {
	x, y := foldTitle(a), foldTitle(b)
	if x == "" || y == "" {
		return 0
	}
	if x == y {
		return 1
	}
	ax, ay := bigramsOf(x), bigramsOf(y)
	if len(ax) == 0 || len(ay) == 0 {
		return 0
	}
	shared := 0
	for g, n := range ax {
		if m, ok := ay[g]; ok {
			shared += min(n, m)
		}
	}
	return 2 * float64(shared) / float64(countOf(ax)+countOf(ay))
}

var notWord = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)

func foldTitle(s string) string {
	s = strings.ToLower(notWord.ReplaceAllString(s, " "))
	return strings.TrimSpace(manySpaces.ReplaceAllString(s, " "))
}

func bigramsOf(s string) map[string]int {
	r := []rune(s)
	m := map[string]int{}
	for i := 0; i+1 < len(r); i++ {
		m[string(r[i:i+2])]++
	}
	return m
}

func countOf(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func (r *DescriptionReport) String() string {
	return fmt.Sprintf("files %d, google %d, asked %d, none found %d, quota hit %v, %s",
		r.FromFiles, r.FromGoogle, r.Asked, r.NotFound, r.QuotaHit,
		r.Elapsed.Round(time.Millisecond))
}

// RunDescriptionFetcher fills in missing descriptions once a day.
//
// Daily rather than continuous because the limit is Google's daily quota, not
// a rate: there is nothing to gain from waking more often, and a job that
// finishes its allowance in an hour and then sleeps is easier to reason about
// than one trickling all day. The files pass runs every time regardless, since
// it costs nothing and covers books added since yesterday.
//
// The first run is delayed: a server that has just started has an import or a
// reorganise to get through, and this is the least urgent thing it does.
func (s *Store) RunDescriptionFetcher(
	ctx context.Context, root string, set *provider.Set, perDay int, log *slog.Logger,
) {
	const settle = 5 * time.Minute
	select {
	case <-ctx.Done():
		return
	case <-time.After(settle):
	}
	for {
		if rep, err := s.FillFromFiles(ctx, root, 100000, false, log); err != nil {
			if ctx.Err() == nil {
				log.Warn("description fill from files failed", "err", err)
			}
		} else if rep.FromFiles > 0 {
			log.Info("descriptions from the books' own files", "filled", rep.FromFiles)
		}

		if set != nil && perDay > 0 {
			rep, err := s.FillFromGoogle(ctx, set, perDay, false, log)
			switch {
			case err != nil && ctx.Err() == nil:
				log.Warn("description fill from Google failed", "err", err)
			case err == nil && (rep.FromGoogle > 0 || rep.Asked > 0):
				log.Info("descriptions from Google Books",
					"filled", rep.FromGoogle, "asked", rep.Asked,
					"no_match", rep.NotFound, "quota_hit", rep.QuotaHit)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
