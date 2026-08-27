package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// libris queries Libris, the Swedish national union catalogue run by
// Kungliga biblioteket.
//
// It holds no cover images -- its own cover service answers noimage.jpg for
// every ISBN in this library that was tried -- so it is not here for pictures.
// It is here for ISBNs. Roughly six thousand books in this library have none,
// and an ISBN is the key Google Books is searched by for descriptions and the
// key that finds the right edition's cover. Libris had a record with an ISBN
// for 91% of a random sample, which is a better rate than either of the
// commercial sources managed for covers.
//
// It is also simply the most authoritative source for Swedish bibliographic
// data: publisher, year and language come from the national library rather
// than from a shop's product page.
type libris struct{}

func (l *libris) Name() string { return "Libris" }

type librisResponse struct {
	XSearch struct {
		Records int               `json:"records"`
		List    []json.RawMessage `json:"list"`
	} `json:"xsearch"`
}

// Libris returns either a string or an array of strings for most fields,
// depending on how many values the record holds.
type librisValue []string

func (v *librisValue) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*v = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*v = many
	return nil
}

func (v librisValue) first() string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

type librisRecord struct {
	Title     librisValue `json:"title"`
	Creator   librisValue `json:"creator"`
	Date      librisValue `json:"date"`
	Publisher librisValue `json:"publisher"`
	ISBN      librisValue `json:"isbn"`
	Language  librisValue `json:"language"`
	Type      librisValue `json:"type"`
}

func (l *libris) Search(ctx context.Context, q Query, limit int) ([]Result, error) {
	term := strings.TrimSpace(q.ISBN)
	if term == "" {
		term = strings.TrimSpace(q.Title + " " + q.Author)
	}
	if term == "" {
		return nil, nil
	}

	v := url.Values{}
	v.Set("query", term)
	v.Set("format", "json")
	v.Set("n", fmt.Sprint(min(limit, 20)))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://libris.kb.se/xsearch?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "KlarasLibrary/1.0 (self-hosted ebook library)")

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 500 {
		return nil, ErrUnavailable
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("libris returned %d", res.StatusCode)
	}

	var lr librisResponse
	if err := json.NewDecoder(res.Body).Decode(&lr); err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(lr.XSearch.List))
	for _, raw := range lr.XSearch.List {
		var rec librisRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			// One malformed record should not lose the rest of the page.
			continue
		}
		title := rec.Title.first()
		if title == "" {
			continue
		}
		r := Result{
			Source:      l.Name(),
			Title:       title,
			Publisher:   rec.Publisher.first(),
			Language:    rec.Language.first(),
			Identifiers: map[string]string{},
		}
		if a := rec.Creator.first(); a != "" {
			r.Authors = []string{librisName(a)}
		}
		if y := librisYear(rec.Date); y != "" {
			r.PubDate = y
		}
		if isbn := rec.ISBN.first(); isbn != "" {
			r.Identifiers["isbn"] = normaliseISBN(isbn)
		}
		out = append(out, r)
	}
	return out, nil
}

// librisName turns the catalogue's "Strindberg, August, 1849-1912" into the
// "August Strindberg" the rest of the app and the file tree use.
func librisName(s string) string {
	s = strings.TrimSpace(s)
	// Drop the life dates a library catalogue appends to disambiguate people.
	if i := strings.LastIndex(s, ","); i > 0 {
		if tail := strings.TrimSpace(s[i+1:]); tail != "" && strings.ContainsAny(tail, "0123456789") {
			s = strings.TrimSpace(s[:i])
		}
	}
	last, first, found := strings.Cut(s, ",")
	if !found {
		return s
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	if first == "" {
		return last
	}
	return first + " " + last
}

// librisYear picks a four-digit year out of the several shapes the date field
// arrives in: "2007", "[2025]", "cop. 1998".
func librisYear(v librisValue) string {
	for _, d := range v {
		digits := make([]rune, 0, 4)
		for _, r := range d {
			if r >= '0' && r <= '9' {
				digits = append(digits, r)
				if len(digits) == 4 {
					return string(digits)
				}
				continue
			}
			digits = digits[:0]
		}
	}
	return ""
}
