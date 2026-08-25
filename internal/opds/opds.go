// Package opds serves an OPDS catalogue.
//
// OPDS is how KOReader, Moon+ Reader, Aldiko and similar apps browse and
// download from a library. Both versions are served: 1.2 is Atom XML and is
// what almost every existing reader speaks; 2.0 is JSON and is what newer ones
// prefer.
package opds

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Karlmit/Klaras-Library/internal/auth"
	"github.com/Karlmit/Klaras-Library/internal/library"
)

// Content types defined by the OPDS specification.
const (
	navigationType = "application/atom+xml;profile=opds-catalog;kind=navigation"
	acquisitionTyp = "application/atom+xml;profile=opds-catalog;kind=acquisition"
	opds2Type      = "application/opds+json"
)

// Handler serves the catalogue.
type Handler struct {
	lib         *library.Store
	auth        *auth.Service
	externalURL string
}

// New builds an OPDS handler.
func New(lib *library.Store, a *auth.Service, externalURL string) *Handler {
	return &Handler{lib: lib, auth: a, externalURL: strings.TrimRight(externalURL, "/")}
}

// Routes mounts the catalogue.
func (h *Handler) Routes(r chi.Router) {
	r.Route("/opds", func(r chi.Router) {
		// HTTP Basic rather than a session: OPDS clients have no cookie jar
		// and no login form, so Basic is what the ecosystem actually uses.
		r.Use(h.basicAuth)

		r.Get("/", h.handleRoot)
		r.Get("/new", h.handleFeed("new", "Recently added", library.SortAdded))
		r.Get("/titles", h.handleFeed("titles", "All books by title", library.SortTitle))
		r.Get("/authors", h.handleGroup("author"))
		r.Get("/series", h.handleGroup("series"))
		r.Get("/tags", h.handleGroup("tag"))
		r.Get("/search", h.handleSearch)
		r.Get("/opensearch.xml", h.handleOpenSearch)

		// OPDS 2.0 (JSON).
		r.Get("/v2", h.handleRootV2)
		r.Get("/v2/books", h.handleFeedV2)
	})
}

// basicAuth authenticates an OPDS client.
func (h *Handler) basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			h.challenge(w)
			return
		}
		u, err := h.auth.Authenticate(r.Context(), user, pass)
		if err != nil {
			h.challenge(w)
			return
		}
		if u.PasswordResetRequired {
			http.Error(w, "set a password in the web interface first", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(withUser(r.Context(), u)))
	})
}

func (h *Handler) challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Klaras Library", charset="UTF-8"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

// --- Atom (OPDS 1.2) --------------------------------------------------------

type feed struct {
	XMLName   xml.Name `xml:"feed"`
	Xmlns     string   `xml:"xmlns,attr"`
	XmlnsDc   string   `xml:"xmlns:dc,attr"`
	XmlnsOpds string   `xml:"xmlns:opds,attr"`
	ID        string   `xml:"id"`
	Title     string   `xml:"title"`
	Updated   string   `xml:"updated"`
	Author    *author  `xml:"author,omitempty"`
	Links     []link   `xml:"link"`
	Entries   []entry  `xml:"entry"`
}

type author struct {
	Name string `xml:"name"`
	URI  string `xml:"uri,omitempty"`
}

type link struct {
	Rel    string `xml:"rel,attr,omitempty"`
	Href   string `xml:"href,attr"`
	Type   string `xml:"type,attr,omitempty"`
	Title  string `xml:"title,attr,omitempty"`
	Length int64  `xml:"length,attr,omitempty"`
}

type entry struct {
	ID        string   `xml:"id"`
	Title     string   `xml:"title"`
	Updated   string   `xml:"updated"`
	Authors   []author `xml:"author,omitempty"`
	Content   *content `xml:"content,omitempty"`
	Language  string   `xml:"dc:language,omitempty"`
	Issued    string   `xml:"dc:issued,omitempty"`
	Publisher string   `xml:"dc:publisher,omitempty"`
	Links     []link   `xml:"link"`
}

type content struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

func newFeed(id, title string, self string) *feed {
	return &feed{
		Xmlns:     "http://www.w3.org/2005/Atom",
		XmlnsDc:   "http://purl.org/dc/terms/",
		XmlnsOpds: "http://opds-spec.org/2010/catalog",
		ID:        id,
		Title:     title,
		Updated:   time.Now().UTC().Format(time.RFC3339),
		Author:    &author{Name: "Klaras Library"},
		Links: []link{
			{Rel: "self", Href: self, Type: acquisitionTyp},
			{Rel: "start", Href: "/opds/", Type: navigationType},
			{Rel: "search", Href: "/opds/opensearch.xml", Type: "application/opensearchdescription+xml"},
		},
	}
}

func writeXML(w http.ResponseWriter, contentType string, v any) {
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		return
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(v)
}

func (h *Handler) handleRoot(w http.ResponseWriter, r *http.Request) {
	f := newFeed("urn:klaras:root", "Klaras Library", "/opds/")
	f.Links[0].Type = navigationType

	nav := []struct{ href, title, desc string }{
		{"/opds/new", "Recently added", "The newest books in the library"},
		{"/opds/titles", "All books", "Everything, by title"},
		{"/opds/authors", "Authors", "Browse by author"},
		{"/opds/series", "Series", "Browse by series"},
		{"/opds/tags", "Categories", "Browse by category"},
	}
	for _, n := range nav {
		f.Entries = append(f.Entries, entry{
			ID:      "urn:klaras:nav:" + n.href,
			Title:   n.title,
			Updated: f.Updated,
			Content: &content{Type: "text", Body: n.desc},
			Links:   []link{{Rel: "subsection", Href: n.href, Type: navigationType}},
		})
	}
	writeXML(w, navigationType, f)
}

// handleFeed serves a paginated acquisition feed.
func (h *Handler) handleFeed(id, title string, sort library.SortMode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := library.Filter{
			Sort:   sort,
			Limit:  50,
			Cursor: q.Get("cursor"),
			Author: q.Get("author"),
			Series: q.Get("series"),
			Tag:    q.Get("tag"),
			Query:  q.Get("q"),
		}
		page, err := h.lib.ListBooks(r.Context(), f)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		self := r.URL.RequestURI()
		feed := newFeed("urn:klaras:"+id, title, self)
		for i := range page.Items {
			feed.Entries = append(feed.Entries, h.bookEntry(&page.Items[i]))
		}
		// OPDS paginates with rel="next", which is a natural fit for keyset
		// cursors: no page numbers, no OFFSET.
		if page.NextCursor != "" {
			nq := r.URL.Query()
			nq.Set("cursor", page.NextCursor)
			feed.Links = append(feed.Links, link{
				Rel:  "next",
				Href: r.URL.Path + "?" + nq.Encode(),
				Type: acquisitionTyp,
			})
		}
		writeXML(w, acquisitionTyp, feed)
	}
}

func (h *Handler) bookEntry(b *library.BookListItem) entry {
	e := entry{
		ID:      "urn:uuid:" + b.UUID,
		Title:   b.Title,
		Updated: b.AddedAt,
	}
	for _, a := range b.Authors {
		e.Authors = append(e.Authors, author{Name: a})
	}
	if b.Series != nil {
		e.Content = &content{Type: "text", Body: fmt.Sprintf("%s%s", *b.Series,
			seriesSuffix(b.SeriesIndex))}
	}
	e.Links = append(e.Links,
		link{Rel: "http://opds-spec.org/image", Href: fmt.Sprintf("/api/books/%d/cover/detail", b.ID), Type: "image/jpeg"},
		link{Rel: "http://opds-spec.org/image/thumbnail", Href: fmt.Sprintf("/api/books/%d/cover/grid", b.ID), Type: "image/jpeg"},
		link{Rel: "http://opds-spec.org/acquisition", Href: fmt.Sprintf("/api/books/%d/download/epub", b.ID), Type: "application/epub+zip"},
	)
	return e
}

func seriesSuffix(idx *float64) string {
	if idx == nil {
		return ""
	}
	return fmt.Sprintf(" #%g", *idx)
}

// handleGroup lists facet values as navigation entries.
func (h *Handler) handleGroup(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		facets, err := h.lib.Facets(r.Context(), 500)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		var values []library.Facet
		var title, param string
		switch kind {
		case "author":
			values, title, param = facets.Authors, "Authors", "author"
		case "series":
			values, title, param = facets.Series, "Series", "series"
		default:
			values, title, param = facets.Tags, "Categories", "tag"
		}

		f := newFeed("urn:klaras:"+kind, title, r.URL.RequestURI())
		f.Links[0].Type = navigationType
		for _, v := range values {
			f.Entries = append(f.Entries, entry{
				ID:      "urn:klaras:" + kind + ":" + v.Value,
				Title:   v.Value,
				Updated: f.Updated,
				Content: &content{Type: "text", Body: fmt.Sprintf("%d books", v.Count)},
				Links: []link{{
					Rel:  "subsection",
					Href: "/opds/titles?" + param + "=" + urlQueryEscape(v.Value),
					Type: acquisitionTyp,
				}},
			})
		}
		writeXML(w, navigationType, f)
	}
}

func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	h.handleFeed("search", "Search results", library.SortRelevant)(w, r)
}

// handleOpenSearch advertises the search endpoint so clients get a search box.
func (h *Handler) handleOpenSearch(w http.ResponseWriter, r *http.Request) {
	type osURL struct {
		XMLName  xml.Name `xml:"Url"`
		Type     string   `xml:"type,attr"`
		Template string   `xml:"template,attr"`
	}
	type osDesc struct {
		XMLName       xml.Name `xml:"OpenSearchDescription"`
		Xmlns         string   `xml:"xmlns,attr"`
		ShortName     string   `xml:"ShortName"`
		Description   string   `xml:"Description"`
		InputEncoding string   `xml:"InputEncoding"`
		URL           osURL    `xml:"Url"`
	}
	writeXML(w, "application/opensearchdescription+xml", osDesc{
		Xmlns:         "http://a9.com/-/spec/opensearch/1.1/",
		ShortName:     "Klaras Library",
		Description:   "Search Klaras Library",
		InputEncoding: "UTF-8",
		URL: osURL{
			Type:     acquisitionTyp,
			Template: "/opds/search?q={searchTerms}",
		},
	})
}

func urlQueryEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(
		strings.ReplaceAll(s, "&", "%26"), "?", "%3F"), " ", "+")
}
