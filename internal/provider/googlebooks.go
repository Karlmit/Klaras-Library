package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// googleBooks queries the public Google Books API.
//
// A key is optional. Without one the quota is shared per-IP and is exhausted
// quickly -- filling thousands of missing descriptions runs into HTTP 429 --
// so bulk work wants a key, while an occasional manual lookup does not.
type googleBooks struct {
	lang string
	key  string
}

func (g *googleBooks) Name() string { return "Google Books" }

type gbResponse struct {
	Items []struct {
		VolumeInfo struct {
			Title               string   `json:"title"`
			Subtitle            string   `json:"subtitle"`
			Authors             []string `json:"authors"`
			Publisher           string   `json:"publisher"`
			PublishedDate       string   `json:"publishedDate"`
			Description         string   `json:"description"`
			Categories          []string `json:"categories"`
			Language            string   `json:"language"`
			IndustryIdentifiers []struct {
				Type       string `json:"type"`
				Identifier string `json:"identifier"`
			} `json:"industryIdentifiers"`
			ImageLinks struct {
				Thumbnail      string `json:"thumbnail"`
				SmallThumbnail string `json:"smallThumbnail"`
			} `json:"imageLinks"`
		} `json:"volumeInfo"`
	} `json:"items"`
}

func (g *googleBooks) Search(ctx context.Context, q Query, limit int) ([]Result, error) {
	var terms []string
	if q.ISBN != "" {
		terms = append(terms, "isbn:"+normaliseISBN(q.ISBN))
	} else {
		if q.Title != "" {
			terms = append(terms, `intitle:`+quotePhrase(q.Title))
		}
		if q.Author != "" {
			terms = append(terms, `inauthor:`+quotePhrase(q.Author))
		}
	}
	if len(terms) == 0 {
		return nil, nil
	}

	u := url.URL{Scheme: "https", Host: "www.googleapis.com", Path: "/books/v1/volumes"}
	v := url.Values{}
	v.Set("q", strings.Join(terms, "+"))
	v.Set("maxResults", fmt.Sprint(min(limit*2, 40)))
	if g.lang != "" {
		v.Set("langRestrict", twoLetter(g.lang))
	}
	if g.key != "" {
		v.Set("key", g.key)
	}
	u.RawQuery = v.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		return nil, ErrQuota
	case res.StatusCode >= 500:
		// Google answers overload with 503 far more often than 429. Reported as
		// a plain error, a caller would record the book as having no
		// description -- a permanent answer to a temporary problem.
		return nil, ErrUnavailable
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("google books returned %d", res.StatusCode)
	}

	var gb gbResponse
	if err := json.NewDecoder(res.Body).Decode(&gb); err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(gb.Items))
	for _, it := range gb.Items {
		vi := it.VolumeInfo
		title := vi.Title
		if vi.Subtitle != "" {
			title += ": " + vi.Subtitle
		}
		r := Result{
			Source: g.Name(), Title: title, Authors: vi.Authors,
			Publisher: vi.Publisher, PubDate: vi.PublishedDate,
			Description: vi.Description, Tags: vi.Categories,
			Language:    threeLetter(vi.Language),
			Identifiers: map[string]string{},
		}
		for _, id := range vi.IndustryIdentifiers {
			switch id.Type {
			case "ISBN_13":
				r.Identifiers["isbn"] = id.Identifier
			case "ISBN_10":
				if r.Identifiers["isbn"] == "" {
					r.Identifiers["isbn"] = id.Identifier
				}
			}
		}
		if t := vi.ImageLinks.Thumbnail; t != "" {
			// Google serves a small thumbnail by default; asking for a larger
			// zoom gives something usable as an actual cover.
			r.CoverURL = strings.Replace(strings.Replace(t, "http://", "https://", 1),
				"&zoom=1", "&zoom=2", 1)
		}
		out = append(out, r)
	}
	return out, nil
}

// quotePhrase wraps a phrase for the Google Books query language, so a
// multi-word title is matched as a phrase rather than as loose keywords.
func quotePhrase(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, "") + `"`
}

func twoLetter(l string) string {
	m := map[string]string{"swe": "sv", "eng": "en", "dan": "da", "nor": "no",
		"deu": "de", "fra": "fr", "spa": "es", "fin": "fi", "ara": "ar"}
	if v, ok := m[l]; ok {
		return v
	}
	if len(l) == 2 {
		return l
	}
	return ""
}

func threeLetter(l string) string {
	m := map[string]string{"sv": "swe", "en": "eng", "da": "dan", "no": "nor",
		"de": "deu", "fr": "fra", "es": "spa", "fi": "fin", "ar": "ara"}
	if v, ok := m[l]; ok {
		return v
	}
	return l
}
