// Package provider looks up book metadata from external sources.
package provider

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Result is one candidate match from a provider.
// ErrQuota is returned when a provider refuses because its daily allowance is
// spent. A caller filling thousands of records needs to stop for the day rather
// than burn through the rest of its list getting 429s.
var ErrQuota = errors.New("provider daily quota exhausted")

type Result struct {
	Source      string            `json:"source"`
	Title       string            `json:"title"`
	Authors     []string          `json:"authors,omitempty"`
	Series      string            `json:"series,omitempty"`
	SeriesIndex *float64          `json:"series_index,omitempty"`
	Description string            `json:"description,omitempty"`
	Publisher   string            `json:"publisher,omitempty"`
	PubDate     string            `json:"pubdate,omitempty"`
	Language    string            `json:"language,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Identifiers map[string]string `json:"identifiers,omitempty"`
	CoverURL    string            `json:"cover_url,omitempty"`
	// Score is a rough confidence, used only to order candidates.
	Score float64 `json:"score"`
}

// Query is what we know about the book already.
type Query struct {
	Title  string
	Author string
	ISBN   string
	Lang   string
}

// Provider is one metadata source.
type Provider interface {
	Name() string
	Search(ctx context.Context, q Query, limit int) ([]Result, error)
}

// client is shared by all providers. The timeout is deliberate: a metadata
// lookup is a convenience, and must never hold up the request that triggered it.
var client = &http.Client{Timeout: 12 * time.Second}

// Set runs several providers together.
type Set struct {
	providers []Provider
}

// NewSet builds the default provider set.
//
// Both are free and need no API key, which matters for a self-hosted app: a
// setup step that requires registering for a key is a setup step most people
// never complete. Google Books has better coverage of Swedish titles; Open
// Library is a good second opinion and has better series data.
func NewSet(lang string) *Set { return NewSetWithKey(lang, "") }

// NewSetWithKey is NewSet with a Google Books API key, which raises the daily
// quota from the shared per-IP allowance to 1,000 lookups of your own.
func NewSetWithKey(lang, googleKey string) *Set {
	return &Set{providers: []Provider{
		&googleBooks{lang: lang, key: googleKey},
		&openLibrary{},
	}}
}

// Search queries every provider and returns the combined candidates, best
// first. A provider that fails is skipped rather than failing the whole lookup.
func (s *Set) Search(ctx context.Context, q Query, limit int) []Result {
	if limit <= 0 {
		limit = 10
	}
	type out struct {
		res []Result
		err error
	}
	ch := make(chan out, len(s.providers))
	for _, p := range s.providers {
		go func(p Provider) {
			r, err := p.Search(ctx, q, limit)
			ch <- out{r, err}
		}(p)
	}

	var all []Result
	for range s.providers {
		o := <-ch
		if o.err != nil {
			continue
		}
		all = append(all, o.res...)
	}

	scoreAll(all, q)
	sortByScore(all)
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// Names lists the configured providers.
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.providers))
	for _, p := range s.providers {
		out = append(out, p.Name())
	}
	return out
}

// SearchOne is Search with the errors kept.
//
// Search deliberately swallows a provider's failure and returns whatever the
// others found, which is right for a person clicking "look this up" -- one
// slow provider should not cost them the result. It is wrong for a job working
// through ten thousand books: a quota refusal has to stop the run, not read as
// "no match" and burn the rest of the list on 429s while recording every book
// as tried.
func (s *Set) SearchOne(ctx context.Context, q Query, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = 5
	}
	var all []Result
	var quota bool
	var firstErr error
	for _, p := range s.providers {
		r, err := p.Search(ctx, q, limit)
		if errors.Is(err, ErrQuota) {
			quota = true
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		all = append(all, r...)
	}
	if len(all) == 0 {
		if quota {
			return nil, ErrQuota
		}
		if firstErr != nil {
			return nil, firstErr
		}
	}
	scoreAll(all, q)
	sortByScore(all)
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}
