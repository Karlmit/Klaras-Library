package opds

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Karlmit/Klaras-Library/internal/library"
)

// OPDS 2.0 is JSON rather than Atom. Newer readers prefer it, and it is far
// cheaper to produce and parse; 1.2 stays because most existing clients only
// speak that.

type v2Link struct {
	Rel   string `json:"rel,omitempty"`
	Href  string `json:"href"`
	Type  string `json:"type,omitempty"`
	Title string `json:"title,omitempty"`
}

type v2Metadata struct {
	Type          string    `json:"@type,omitempty"`
	Title         string    `json:"title"`
	Author        []string  `json:"author,omitempty"`
	Identifier    string    `json:"identifier,omitempty"`
	Language      string    `json:"language,omitempty"`
	Published     string    `json:"published,omitempty"`
	Description   string    `json:"description,omitempty"`
	BelongsTo     *v2Series `json:"belongsTo,omitempty"`
	NumberOfItems int64     `json:"numberOfItems,omitempty"`
}

type v2Series struct {
	Series struct {
		Name     string  `json:"name"`
		Position float64 `json:"position,omitempty"`
	} `json:"series"`
}

type v2Publication struct {
	Metadata v2Metadata `json:"metadata"`
	Links    []v2Link   `json:"links"`
	Images   []v2Link   `json:"images,omitempty"`
}

type v2Group struct {
	Metadata     v2Metadata      `json:"metadata"`
	Links        []v2Link        `json:"links,omitempty"`
	Navigation   []v2Link        `json:"navigation,omitempty"`
	Publications []v2Publication `json:"publications,omitempty"`
}

type v2Feed struct {
	Metadata     v2Metadata      `json:"metadata"`
	Links        []v2Link        `json:"links"`
	Navigation   []v2Link        `json:"navigation,omitempty"`
	Publications []v2Publication `json:"publications,omitempty"`
	Groups       []v2Group       `json:"groups,omitempty"`
}

func writeOPDS2(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", opds2Type)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) handleRootV2(w http.ResponseWriter, r *http.Request) {
	writeOPDS2(w, v2Feed{
		Metadata: v2Metadata{Title: "Klaras Library"},
		Links: []v2Link{
			{Rel: "self", Href: "/opds/v2", Type: opds2Type},
			{Rel: "search", Href: "/opds/v2/books?q={searchTerms}", Type: opds2Type},
		},
		Navigation: []v2Link{
			{Rel: "current", Href: "/opds/v2/books?sort=added", Type: opds2Type, Title: "Recently added"},
			{Href: "/opds/v2/books?sort=title", Type: opds2Type, Title: "All books"},
		},
	})
}

func (h *Handler) handleFeedV2(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sort := library.SortMode(q.Get("sort"))
	if sort == "" {
		sort = library.SortTitle
	}
	if q.Get("q") != "" {
		sort = library.SortRelevant
	}

	page, err := h.lib.ListBooks(r.Context(), library.Filter{
		Query:  q.Get("q"),
		Author: q.Get("author"),
		Tag:    q.Get("tag"),
		Series: q.Get("series"),
		Sort:   sort,
		Limit:  50,
		Cursor: q.Get("cursor"),
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	feed := v2Feed{
		Metadata: v2Metadata{Title: "Klaras Library"},
		Links:    []v2Link{{Rel: "self", Href: r.URL.RequestURI(), Type: opds2Type}},
	}
	for i := range page.Items {
		b := &page.Items[i]
		pub := v2Publication{
			Metadata: v2Metadata{
				Type:       "http://schema.org/Book",
				Title:      b.Title,
				Author:     b.Authors,
				Identifier: "urn:uuid:" + b.UUID,
			},
			Links: []v2Link{{
				Rel:  "http://opds-spec.org/acquisition",
				Href: fmt.Sprintf("/api/books/%d/download/epub", b.ID),
				Type: "application/epub+zip",
			}},
			Images: []v2Link{
				{Href: fmt.Sprintf("/api/books/%d/cover/detail", b.ID), Type: "image/jpeg"},
				{Href: fmt.Sprintf("/api/books/%d/cover/grid", b.ID), Type: "image/jpeg"},
			},
		}
		if b.Series != nil {
			s := &v2Series{}
			s.Series.Name = *b.Series
			if b.SeriesIndex != nil {
				s.Series.Position = *b.SeriesIndex
			}
			pub.Metadata.BelongsTo = s
		}
		feed.Publications = append(feed.Publications, pub)
	}
	if page.NextCursor != "" {
		nq := r.URL.Query()
		nq.Set("cursor", page.NextCursor)
		feed.Links = append(feed.Links, v2Link{
			Rel: "next", Href: r.URL.Path + "?" + nq.Encode(), Type: opds2Type,
		})
	}
	writeOPDS2(w, feed)
}
