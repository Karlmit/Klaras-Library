// Package provider looks up book metadata from external sources.
package provider

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Result is one candidate match from a provider.
// ErrQuota is returned when a provider refuses because its daily allowance is
// spent. A caller filling thousands of records needs to stop for the day rather
// than burn through the rest of its list getting 429s.
var ErrQuota = errors.New("provider daily quota exhausted")

// ErrUnavailable is returned when a provider is temporarily refusing service --
// a 5xx, a timeout, a reset connection.
//
// Deliberately distinct from "no match". A caller recording what it has already
// asked about must not write down "this book has no description" because the
// service was having a bad minute: that answer is permanent and would never be
// revisited. Google in particular answers overload with 503, not 429.
var ErrUnavailable = errors.New("provider temporarily unavailable")

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
// All three are free and need no API key, which matters for a self-hosted app:
// a setup step that requires registering for a key is a setup step most people
// never complete. Apple Books has the best hit rate on Swedish titles and much
// the largest cover art; Google Books is close behind and is the one with
// blurbs for ISBN lookups; Open Library is weak here but has the best series
// data and reaches older and more obscure records the other two miss.
func NewSet(lang string) *Set { return NewSetWithKey(lang, "") }

// NewSetWithKey is NewSet with a Google Books API key, which raises the daily
// quota from the shared per-IP allowance to 1,000 lookups of your own.
func NewSetWithKey(lang, googleKey string) *Set {
	return &Set{providers: []Provider{
		&googleBooks{lang: lang, key: googleKey},
		newAppleBooks(lang),
		&openLibrary{},
	}}
}

// ProviderStatus is what one provider did on a search: how many candidates it
// contributed, or why it contributed none.
//
// A provider that fails is skipped rather than failing the whole lookup, which
// is right -- one slow source should not cost someone the other's results. But
// skipped silently, "Google Books is out of quota today" and "Google Books has
// never heard of this book" look identical, and the second sends someone
// editing metadata by hand for no reason.
type ProviderStatus struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Error string `json:"error,omitempty"`
}

// Search queries every provider and returns the combined candidates, best
// first. A provider that fails is skipped rather than failing the whole lookup.
func (s *Set) Search(ctx context.Context, q Query, limit int) []Result {
	res, _ := s.SearchWithStatus(ctx, q, limit)
	return res
}

// SearchWithStatus is Search, with an account of what each provider did.
func (s *Set) SearchWithStatus(ctx context.Context, q Query, limit int) ([]Result, []ProviderStatus) {
	if limit <= 0 {
		limit = 10
	}
	type out struct {
		name string
		res  []Result
		err  error
	}
	ch := make(chan out, len(s.providers))
	for _, p := range s.providers {
		go func(p Provider) {
			r, err := p.Search(ctx, q, limit)
			ch <- out{p.Name(), r, err}
		}(p)
	}

	// Non-nil from the start. This value is marshalled straight into a JSON
	// response, and a nil slice becomes `null`, not `[]` -- which every client
	// then has to remember to guard. One did not, and a lookup that found
	// nothing took the whole page down with it.
	all := []Result{}
	status := make([]ProviderStatus, 0, len(s.providers))
	for range s.providers {
		o := <-ch
		st := ProviderStatus{Name: o.name, Count: len(o.res)}
		switch {
		case errors.Is(o.err, ErrQuota):
			st.Error = "daily quota reached"
		case errors.Is(o.err, ErrUnavailable):
			st.Error = "not answering just now"
		case o.err != nil:
			st.Error = "search failed"
		}
		status = append(status, st)
		if o.err != nil {
			continue
		}
		all = append(all, o.res...)
	}
	sort.Slice(status, func(i, j int) bool { return status[i].Name < status[j].Name })

	scoreAll(all, q)
	sortByScore(all)
	if len(all) > limit {
		all = all[:limit]
	}
	return all, status
}

// Only narrows the set to one provider by name.
//
// Both providers are queried together by default and their results merged, so
// this cannot find anything the default search would miss. It is for looking
// at one source on its own: when a result is wrong it is useful to know which
// of them said it, and Open Library's series data and Google's blurbs are
// worth comparing side by side.
//
// An unknown name returns an empty set rather than silently falling back to
// everything, so a typo shows up as no results instead of as a search that
// quietly ignored the request.
func (s *Set) Only(name string) *Set {
	out := &Set{}
	for _, p := range s.providers {
		if strings.EqualFold(p.Name(), name) {
			out.providers = append(out.providers, p)
		}
	}
	return out
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
	var quota, unavailable bool
	var firstErr error
	for _, p := range s.providers {
		r, err := p.Search(ctx, q, limit)
		switch {
		case errors.Is(err, ErrQuota):
			quota = true
			continue
		case errors.Is(err, ErrUnavailable):
			unavailable = true
			continue
		case err != nil:
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		all = append(all, r...)
	}
	if len(all) == 0 {
		switch {
		case quota:
			return nil, ErrQuota
		case unavailable:
			return nil, ErrUnavailable
		case firstErr != nil:
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

// NewSetOf builds a Set from explicit providers. For tests, which need to
// exercise how a caller handles a provider that is down or out of quota
// without waiting for the real service to be in that state.
func NewSetOf(ps ...Provider) *Set { return &Set{providers: ps} }
