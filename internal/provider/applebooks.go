package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// appleBooks queries the iTunes Search API.
//
// Added because it is markedly the best source for this library. Measured over
// a random sample of 24 Swedish books: Apple found a cover for 83% of them,
// Google Books 66%, Open Library 4%. It needs no key, it carries the Swedish
// storefront's own metadata, and its cover art is 920x1400 where Open Library's
// "large" is often a third of that.
//
// It matters most for the books with no ISBN. Google is searched by ISBN for
// blurbs, which leaves several thousand records here unreachable; Apple matches
// on title and author and returns a description with the result.
type appleBooks struct {
	// storefront is the country whose catalogue is searched. One is enough:
	// the Swedish store carries English-language titles too.
	storefront string
	limiter    *bucket
}

func newAppleBooks(lang string) *appleBooks {
	store := "se"
	if lang != "" && !strings.HasPrefix(strings.ToLower(lang), "sv") {
		store = "us"
	}
	// Apple asks for about 20 calls a minute. A person clicking "look this up"
	// spends one; the nightly description job would spend all of them, so the
	// bucket is what keeps the interactive path working while a bulk run is
	// going. Refusing is safe: an exhausted bucket reports ErrUnavailable,
	// which callers already treat as "ask again later" rather than recording
	// the book as having no description.
	return &appleBooks{storefront: store, limiter: newBucket(20, time.Minute)}
}

func (a *appleBooks) Name() string { return "Apple Books" }

type appleResponse struct {
	Results []struct {
		TrackName    string   `json:"trackName"`
		ArtistName   string   `json:"artistName"`
		Description  string   `json:"description"`
		ReleaseDate  string   `json:"releaseDate"`
		Genres       []string `json:"genres"`
		ArtworkURL   string   `json:"artworkUrl100"`
		TrackViewURL string   `json:"trackViewUrl"`
	} `json:"results"`
}

// artworkSize rewrites the thumbnail path Apple returns into a full-size one.
// The dimensions are a path segment, so the same image is available at any
// size by asking for it.
var artworkSize = regexp.MustCompile(`/\d+x\d+bb\.(jpg|png)$`)

func (a *appleBooks) Search(ctx context.Context, q Query, limit int) ([]Result, error) {
	// No ISBN search: the endpoint has no field for it, and passing one as a
	// free-text term returns unrelated books rather than nothing, which is
	// worse than not asking.
	term := strings.TrimSpace(q.Title + " " + q.Author)
	if term == "" {
		return nil, nil
	}
	if !a.limiter.take() {
		return nil, ErrUnavailable
	}

	v := url.Values{}
	v.Set("term", term)
	v.Set("entity", "ebook")
	v.Set("country", a.storefront)
	v.Set("limit", fmt.Sprint(min(limit, 20)))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://itunes.apple.com/search?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "KlarasLibrary/1.0 (self-hosted ebook library)")

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusTooManyRequests:
		// Apple throttles by IP without documenting the ceiling. Temporary, so
		// never recorded as "this book has nothing".
		return nil, ErrUnavailable
	case res.StatusCode >= 500:
		return nil, ErrUnavailable
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("apple books returned %d", res.StatusCode)
	}

	var ap appleResponse
	if err := json.NewDecoder(res.Body).Decode(&ap); err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(ap.Results))
	for _, it := range ap.Results {
		if it.TrackName == "" {
			continue
		}
		r := Result{
			Source:      a.Name(),
			Title:       it.TrackName,
			Description: cleanHTML(it.Description),
			Identifiers: map[string]string{},
		}
		if it.ArtistName != "" {
			r.Authors = []string{it.ArtistName}
		}
		if len(it.ReleaseDate) >= 10 {
			r.PubDate = it.ReleaseDate[:10]
		}
		for _, g := range it.Genres {
			// "Böcker" is on every record and says nothing.
			if !strings.EqualFold(g, "Böcker") && !strings.EqualFold(g, "Books") {
				r.Tags = append(r.Tags, g)
			}
		}
		if it.ArtworkURL != "" {
			r.CoverURL = artworkSize.ReplaceAllString(it.ArtworkURL, "/1400x1400bb.$1")
		}
		out = append(out, r)
	}
	return out, nil
}

// cleanHTML turns Apple's blurb markup into the plain text every other
// provider returns, so a description reads the same wherever it came from.
var (
	htmlBreak = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlPara  = regexp.MustCompile(`(?i)</p\s*>`)
	htmlTag   = regexp.MustCompile(`<[^>]+>`)
)

func cleanHTML(s string) string {
	if s == "" {
		return ""
	}
	// A paragraph is a bigger break than a line break, and flattening both to
	// one newline runs the paragraphs of a blurb together.
	s = htmlPara.ReplaceAllString(s, "\n\n")
	s = htmlBreak.ReplaceAllString(s, "\n")
	s = htmlTag.ReplaceAllString(s, "")
	// The full entity table, not a handful: Swedish blurbs are full of &ouml;,
	// &auml; and &aring;, and a short list of my own would leave those on the
	// page as written.
	s = html.UnescapeString(s)
	return strings.TrimSpace(manyBlankLines.ReplaceAllString(s, "\n\n"))
}

var manyBlankLines = regexp.MustCompile(`\n{3,}`)

// bucket is a token bucket that refills all at once.
type bucket struct {
	mu     sync.Mutex
	tokens int
	size   int
	every  time.Duration
	next   time.Time
}

func newBucket(size int, every time.Duration) *bucket {
	return &bucket{tokens: size, size: size, every: every}
}

func (b *bucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if now.After(b.next) {
		b.tokens = b.size
		b.next = now.Add(b.every)
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}
