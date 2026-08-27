package library

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNoPortrait means the lookup ran and found nothing. Distinct from an error:
// it is an answer, and it is recorded so it is not asked again.
var ErrNoPortrait = errors.New("no portrait for this author")

// portraitClient is separate from the metadata providers' client because these
// requests are triggered by scrolling rather than by a person asking for one
// book, and must never be the reason a page feels slow.
var portraitClient = &http.Client{Timeout: 15 * time.Second}

const portraitAgent = "KlarasLibrary/1.0 (self-hosted ebook library; portraits)"

// PortraitPath returns an author's cached portrait.
//
// Cache only: it never fetches. Serving a page of the authors grid would
// otherwise fire a Wikidata lookup per visible card -- seventy-odd for one
// screen, thousands for a scroll -- which is slow for the person scrolling and
// rude to a free service. RunPortraitFetcher fills the cache in the background
// instead, and the grid asks only for the ones it already has.
func (s *Store) PortraitPath(ctx context.Context, cacheDir string, authorID int64) (string, error) {
	var filename *string
	err := s.pool.QueryRow(ctx,
		`SELECT filename FROM author_portraits WHERE author_id = $1`, authorID).Scan(&filename)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && filename == nil) {
		return "", ErrNoPortrait
	}
	if err != nil {
		return "", err
	}
	p := filepath.Join(cacheDir, "portraits", *filename)
	if _, statErr := os.Stat(p); statErr != nil {
		// The row says there is one but the cache volume was wiped. Forget it,
		// so the sweep collects it again.
		_, _ = s.pool.Exec(ctx, `DELETE FROM author_portraits WHERE author_id = $1`, authorID)
		return "", ErrNoPortrait
	}
	return p, nil
}

// RunPortraitFetcher fills in author portraits in the background, slowly.
//
// Most-published authors first, because those are the cards someone actually
// looks at, and one at a time with a pause between: this is nine thousand
// lookups against a free service that owes us nothing, and there is no hurry.
func (s *Store) RunPortraitFetcher(ctx context.Context, cacheDir string, log *slog.Logger) {
	const (
		settle = 2 * time.Minute
		pace   = 3 * time.Second
		// Long enough that a name added to Wikidata since is eventually
		// found, short enough to be worth doing at all.
		retryAfter = 90 * 24 * time.Hour
	)
	select {
	case <-ctx.Done():
		return
	case <-time.After(settle):
	}

	dir := filepath.Join(cacheDir, "portraits")
	found, missing := 0, 0
	for {
		if ctx.Err() != nil {
			return
		}
		var id int64
		var name string
		err := s.pool.QueryRow(ctx, `
			SELECT a.id, a.name
			FROM authors a
			JOIN book_authors ba ON ba.author_id = a.id
			LEFT JOIN author_portraits ap ON ap.author_id = a.id
			WHERE ap.author_id IS NULL
			   OR (ap.filename IS NULL AND ap.tried_at < now() - $1::interval)
			GROUP BY a.id, a.name
			ORDER BY count(*) DESC
			LIMIT 1`, retryAfter).Scan(&id, &name)
		if errors.Is(err, pgx.ErrNoRows) {
			log.Info("author portraits complete", "found", found, "without", missing)
			// Nothing left today. Check again tomorrow in case authors were added.
			select {
			case <-ctx.Done():
				return
			case <-time.After(24 * time.Hour):
				continue
			}
		}
		if err != nil {
			log.Warn("portrait sweep query failed", "err", err)
			return
		}

		file, src, srcURL, ferr := fetchPortrait(ctx, dir, name)
		if ferr != nil && !errors.Is(ferr, ErrNoPortrait) {
			// A transport failure is not an answer. Leave no row, so this
			// author is picked up again rather than written off.
			log.Warn("portrait fetch failed", "author", name, "err", ferr)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
				continue
			}
		}

		var store *string
		if file != "" {
			store = &file
			found++
		} else {
			missing++
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO author_portraits (author_id, filename, source, source_url, tried_at)
			VALUES ($1,$2,$3,$4,now())
			ON CONFLICT (author_id) DO UPDATE
			SET filename = EXCLUDED.filename, source = EXCLUDED.source,
			    source_url = EXCLUDED.source_url, tried_at = now()`,
			id, store, src, srcURL); err != nil {
			log.Warn("recording portrait failed", "author", name, "err", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(pace):
		}
	}
}

// fetchPortrait asks Wikidata for a person by name and downloads the image its
// P18 claim names.
func fetchPortrait(ctx context.Context, dir, name string) (file, source, srcURL string, err error) {
	name = strings.TrimSpace(name)
	// Names that are not people. This library's most prolific "authors" are
	// imprints and import placeholders, and searching for them returns
	// confident nonsense -- a photograph of somebody unrelated is worse than
	// no picture.
	if name == "" || notAPerson(name) {
		return "", "", "", ErrNoPortrait
	}

	qid, err := wikidataID(ctx, name)
	if err != nil || qid == "" {
		return "", "", "", ErrNoPortrait
	}
	image, err := wikidataImage(ctx, qid)
	if err != nil || image == "" {
		return "", "", "", ErrNoPortrait
	}

	// Special:FilePath resolves a Commons file name to the file itself and
	// takes a width, so this is one request rather than a metadata round trip.
	u := "https://commons.wikimedia.org/wiki/Special:FilePath/" +
		url.PathEscape(image) + "?width=400"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("User-Agent", portraitAgent)
	res, err := portraitClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", "", "", ErrNoPortrait
	}

	head := make([]byte, 512)
	n, _ := io.ReadFull(res.Body, head)
	head = head[:n]
	ct := http.DetectContentType(head)
	if !strings.HasPrefix(ct, "image/") {
		return "", "", "", ErrNoPortrait
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", "", err
	}
	sum := sha1.Sum([]byte(name))
	file = hex.EncodeToString(sum[:]) + extForType(ct)

	tmp, err := os.CreateTemp(dir, ".portrait-*")
	if err != nil {
		return "", "", "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(head); err != nil {
		tmp.Close()
		return "", "", "", err
	}
	if _, err := io.Copy(tmp, io.LimitReader(res.Body, 8<<20)); err != nil {
		tmp.Close()
		return "", "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, file)); err != nil {
		return "", "", "", err
	}
	return file, "Wikidata", u, nil
}

func extForType(ct string) string {
	switch {
	case strings.HasPrefix(ct, "image/png"):
		return ".png"
	case strings.HasPrefix(ct, "image/webp"):
		return ".webp"
	case strings.HasPrefix(ct, "image/gif"):
		return ".gif"
	default:
		return ".jpg"
	}
}

// notAPerson filters the names that are obviously not authors. Cheap and
// deliberately conservative: a wrong photograph is worse than none.
func notAPerson(name string) bool {
	l := strings.ToLower(name)
	for _, s := range []string{
		"unknown", "okänd", "diverse", "various", "anonym", "anonymous",
		"förlag", "forlag", "publishing", "publisher", "redaktion", "media",
		"antologi", "anthology", "n/a",
	} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func wikidataID(ctx context.Context, name string) (string, error) {
	v := url.Values{}
	v.Set("action", "wbsearchentities")
	v.Set("search", name)
	v.Set("language", "sv")
	v.Set("uselang", "sv")
	v.Set("type", "item")
	v.Set("format", "json")
	v.Set("limit", "1")

	var body struct {
		Search []struct {
			ID string `json:"id"`
		} `json:"search"`
	}
	if err := getJSON(ctx, "https://www.wikidata.org/w/api.php?"+v.Encode(), &body); err != nil {
		return "", err
	}
	if len(body.Search) == 0 {
		return "", nil
	}
	return body.Search[0].ID, nil
}

func wikidataImage(ctx context.Context, qid string) (string, error) {
	v := url.Values{}
	v.Set("action", "wbgetclaims")
	v.Set("entity", qid)
	v.Set("property", "P18")
	v.Set("format", "json")

	var body struct {
		Claims struct {
			P18 []struct {
				MainSnak struct {
					DataValue struct {
						Value string `json:"value"`
					} `json:"datavalue"`
				} `json:"mainsnak"`
			} `json:"P18"`
		} `json:"claims"`
	}
	if err := getJSON(ctx, "https://www.wikidata.org/w/api.php?"+v.Encode(), &body); err != nil {
		return "", err
	}
	if len(body.Claims.P18) == 0 {
		return "", nil
	}
	return body.Claims.P18[0].MainSnak.DataValue.Value, nil
}

func getJSON(ctx context.Context, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", portraitAgent)
	res, err := portraitClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("wikidata returned %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(into)
}

// SetPortrait stores an image as an author's portrait, replacing any it had.
//
// Given to the store rather than written by the handler because the cache file
// and the row that points at it have to agree: a file with no row is never
// served, and a row with no file makes the grid ask for a picture that is not
// there.
func (s *Store) SetPortrait(ctx context.Context, cacheDir string, authorID int64, r io.Reader, source string) error {
	dir := filepath.Join(cacheDir, "portraits")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	head := make([]byte, 512)
	n, _ := io.ReadFull(r, head)
	head = head[:n]
	ct := http.DetectContentType(head)
	if !strings.HasPrefix(ct, "image/") {
		return ErrNotAnImage
	}

	var name string
	if err := s.pool.QueryRow(ctx,
		`SELECT name FROM authors WHERE id = $1`, authorID).Scan(&name); err != nil {
		return err
	}
	sum := sha1.Sum([]byte(name))
	// The name carries a counter so a replacement lands on a new URL: portraits
	// are cached hard by the browser, and reusing the filename would leave the
	// old face on screen until that cache expired.
	var seq int64
	_ = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM author_portraits WHERE author_id = $1`, authorID).Scan(&seq)
	file := fmt.Sprintf("%s-%d%s", hex.EncodeToString(sum[:]), time.Now().Unix()+seq, extForType(ct))

	tmp, err := os.CreateTemp(dir, ".portrait-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(head); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, io.LimitReader(r, 8<<20)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, file)); err != nil {
		return err
	}

	old := s.portraitFile(ctx, authorID)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO author_portraits (author_id, filename, source, source_url, tried_at)
		VALUES ($1,$2,$3,NULL,now())
		ON CONFLICT (author_id) DO UPDATE
		SET filename = EXCLUDED.filename, source = EXCLUDED.source,
		    source_url = NULL, tried_at = now()`, authorID, file, source); err != nil {
		return err
	}
	if old != "" && old != file {
		_ = os.Remove(filepath.Join(dir, old))
	}
	return nil
}

// ClearPortrait removes an author's picture and records that they have none, so
// the background sweep does not immediately put the old one back.
func (s *Store) ClearPortrait(ctx context.Context, cacheDir string, authorID int64) error {
	old := s.portraitFile(ctx, authorID)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO author_portraits (author_id, filename, source, source_url, tried_at)
		VALUES ($1, NULL, 'cleared by hand', NULL, now())
		ON CONFLICT (author_id) DO UPDATE
		SET filename = NULL, source = 'cleared by hand', source_url = NULL, tried_at = now()`,
		authorID); err != nil {
		return err
	}
	if old != "" {
		_ = os.Remove(filepath.Join(cacheDir, "portraits", old))
	}
	return nil
}

// LookUpPortrait forgets what is known about an author and searches again, for
// when the sweep found nothing and a name has since been corrected.
func (s *Store) LookUpPortrait(ctx context.Context, cacheDir string, authorID int64) error {
	var name string
	if err := s.pool.QueryRow(ctx,
		`SELECT name FROM authors WHERE id = $1`, authorID).Scan(&name); err != nil {
		return err
	}
	dir := filepath.Join(cacheDir, "portraits")
	file, src, srcURL, err := fetchPortrait(ctx, dir, name)
	if err != nil && !errors.Is(err, ErrNoPortrait) {
		return err
	}
	var store *string
	if file != "" {
		store = &file
	}
	old := s.portraitFile(ctx, authorID)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO author_portraits (author_id, filename, source, source_url, tried_at)
		VALUES ($1,$2,$3,$4,now())
		ON CONFLICT (author_id) DO UPDATE
		SET filename = EXCLUDED.filename, source = EXCLUDED.source,
		    source_url = EXCLUDED.source_url, tried_at = now()`,
		authorID, store, src, srcURL); err != nil {
		return err
	}
	if old != "" && old != file {
		_ = os.Remove(filepath.Join(dir, old))
	}
	if file == "" {
		return ErrNoPortrait
	}
	return nil
}

func (s *Store) portraitFile(ctx context.Context, authorID int64) string {
	var f *string
	if err := s.pool.QueryRow(ctx,
		`SELECT filename FROM author_portraits WHERE author_id = $1`, authorID).Scan(&f); err != nil {
		return ""
	}
	if f == nil {
		return ""
	}
	return *f
}

// ErrNotAnImage is returned when uploaded bytes are not a picture.
var ErrNotAnImage = errors.New("not an image")

// AuthorDetail is one author's own page.
type AuthorDetail struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Sort          string `json:"sort"`
	Books         int    `json:"books"`
	HasPortrait   bool   `json:"has_portrait"`
	PortraitFrom  string `json:"portrait_from,omitempty"`
	PortraitTried bool   `json:"portrait_tried"`
}

// Author reads one author, with where their picture came from.
func (s *Store) Author(ctx context.Context, id int64) (*AuthorDetail, error) {
	var a AuthorDetail
	var src *string
	var filename *string
	var tried *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT a.id, a.name, COALESCE(a.sort, a.name),
		       (SELECT count(*)::int FROM book_authors ba
		         JOIN books b ON b.id = ba.book_id AND NOT b.adult
		        WHERE ba.author_id = a.id),
		       ap.filename, ap.source, ap.tried_at
		FROM authors a
		LEFT JOIN author_portraits ap ON ap.author_id = a.id
		WHERE a.id = $1`, id).
		Scan(&a.ID, &a.Name, &a.Sort, &a.Books, &filename, &src, &tried)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.HasPortrait = filename != nil
	if src != nil {
		a.PortraitFrom = *src
	}
	a.PortraitTried = tried != nil
	return &a, nil
}
