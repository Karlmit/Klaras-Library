package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// openLibrary queries Open Library's search API.
//
// Kept alongside Google Books as a second opinion: its series data is better,
// and it covers older and more obscure titles that Google does not.
type openLibrary struct{}

func (o *openLibrary) Name() string { return "Open Library" }

type olResponse struct {
	Docs []struct {
		Title            string   `json:"title"`
		AuthorName       []string `json:"author_name"`
		FirstPublishYear int      `json:"first_publish_year"`
		Publisher        []string `json:"publisher"`
		ISBN             []string `json:"isbn"`
		Language         []string `json:"language"`
		Subject          []string `json:"subject"`
		CoverI           int      `json:"cover_i"`
		Series           []string `json:"series"`
	} `json:"docs"`
}

func (o *openLibrary) Search(ctx context.Context, q Query, limit int) ([]Result, error) {
	v := url.Values{}
	switch {
	case q.ISBN != "":
		v.Set("isbn", normaliseISBN(q.ISBN))
	case q.Title != "" || q.Author != "":
		if q.Title != "" {
			v.Set("title", q.Title)
		}
		if q.Author != "" {
			v.Set("author", q.Author)
		}
	default:
		return nil, nil
	}
	v.Set("limit", fmt.Sprint(min(limit*2, 40)))
	// Ask only for the fields used below; the default response is enormous.
	v.Set("fields", "title,author_name,first_publish_year,publisher,isbn,language,subject,cover_i,series")

	u := "https://openlibrary.org/search.json?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// Open Library asks for a descriptive User-Agent.
	req.Header.Set("User-Agent", "KlarasLibrary/1.0 (self-hosted ebook library)")

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open library returned %d", res.StatusCode)
	}

	var ol olResponse
	if err := json.NewDecoder(res.Body).Decode(&ol); err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(ol.Docs))
	for _, d := range ol.Docs {
		r := Result{
			Source: o.Name(), Title: d.Title, Authors: d.AuthorName,
			Identifiers: map[string]string{},
		}
		if len(d.Publisher) > 0 {
			r.Publisher = d.Publisher[0]
		}
		if d.FirstPublishYear > 0 {
			r.PubDate = fmt.Sprintf("%d", d.FirstPublishYear)
		}
		if len(d.ISBN) > 0 {
			r.Identifiers["isbn"] = d.ISBN[0]
		}
		if len(d.Language) > 0 {
			r.Language = d.Language[0]
		}
		if len(d.Series) > 0 {
			r.Series = d.Series[0]
		}
		if n := len(d.Subject); n > 0 {
			// Open Library subjects run to hundreds of entries; a handful is
			// useful, the rest is noise.
			if n > 8 {
				n = 8
			}
			r.Tags = d.Subject[:n]
		}
		if d.CoverI > 0 {
			r.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", d.CoverI)
		}
		out = append(out, r)
	}
	return out, nil
}
