// Package filestore owns the managed file tree: where a book's files live, and
// how they move when its metadata changes.
package filestore

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Template describes the layout of the managed tree.
type Template struct {
	// WithSeries is used when the book belongs to a series.
	WithSeries string
	// Plain is used otherwise.
	Plain string
	// File names the files inside the book directory (without extension).
	File string
}

// DefaultTemplate matches the agreed layout: Author/Series/Title, falling back
// to Author/Title. In this library only 81 of 28,038 books are in a series, so
// almost everything lands in the plain form.
func DefaultTemplate() Template {
	return Template{
		WithSeries: "{author_sort}/{series}/{series_index} - {title}",
		Plain:      "{author_sort}/{title}",
		File:       "{title} - {author_sort}",
	}
}

// Meta is the metadata a path is built from.
type Meta struct {
	ID          int64
	Title       string
	AuthorSort  string
	Series      string
	SeriesIndex *float64
	Year        int
}

// illegal matches characters no filesystem should be asked to hold.
//
// Note what is NOT here: å, ä, ö and every other non-ASCII letter. Calibre
// transliterates them away, which is why the existing tree reads "Susanne
// Akesson"; this library is 94% Swedish and its filesystem is UTF-8, so the
// real names are kept.
var illegal = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f]`)

// collapseSpace flattens runs of whitespace.
var collapseSpace = regexp.MustCompile(`\s+`)

// maxComponentBytes keeps each path component inside the 255-byte limit that
// ext4, XFS and ZFS all share. Swedish characters are two bytes in UTF-8, so
// counting runes would not be enough.
const maxComponentBytes = 120

// SanitiseComponent makes one path segment safe without transliterating it.
func SanitiseComponent(s string) string {
	s = illegal.ReplaceAllString(s, " ")
	s = collapseSpace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	// Windows and SMB both dislike trailing dots and spaces, and this library
	// is served over SMB.
	s = strings.TrimRight(s, ". ")

	if s == "" {
		return "Unknown"
	}
	s = truncateBytes(s, maxComponentBytes)

	// Reserved on Windows, and an SMB share may be mounted there.
	switch strings.ToUpper(s) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return s + "_"
	}
	return s
}

// truncateBytes cuts to n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimRight(s[:n], ". ")
}

// formatSeriesIndex renders a series position for a folder name: 01, 02, 2.5.
func formatSeriesIndex(f *float64) string {
	if f == nil {
		return "00"
	}
	if *f == float64(int64(*f)) {
		return fmt.Sprintf("%02d", int64(*f))
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

// Dir returns the directory a book should live in, relative to the library root.
func (t Template) Dir(m Meta) string {
	tpl := t.Plain
	if strings.TrimSpace(m.Series) != "" {
		tpl = t.WithSeries
	}
	rendered := t.expand(tpl, m)

	parts := strings.Split(rendered, "/")
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = SanitiseComponent(p); p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 {
		return SanitiseComponent(fmt.Sprintf("Unknown (%d)", m.ID))
	}
	return filepath.Join(clean...)
}

// FileBase returns the filename stem for a book's files.
func (t Template) FileBase(m Meta) string {
	return SanitiseComponent(t.expand(t.File, m))
}

// sanitiseValue strips characters that would change a path's shape, without the
// per-component rules (truncation, reserved names) that only make sense once
// the template has been assembled.
//
// Applied to values before substitution, never after. Dir splits the rendered
// template on "/" to find its components, so a slash inside an author or title
// -- "Agnes Wold / Cecilia Chrapkowska", "Sveriges statsministrar under 100 år
// / Samlingsvolym" -- became a directory separator and buried the book a level
// deeper than the template says. Roughly 70 books in this library. FileBase
// never showed it, because it sanitises the whole assembled string.
func sanitiseValue(s string) string {
	s = illegal.ReplaceAllString(s, " ")
	return strings.TrimSpace(collapseSpace.ReplaceAllString(s, " "))
}

func (t Template) expand(tpl string, m Meta) string {
	author := sanitiseValue(m.AuthorSort)
	if author == "" {
		author = "Unknown"
	}
	title := sanitiseValue(m.Title)
	if title == "" {
		title = "Unknown"
	}
	r := strings.NewReplacer(
		"{author_sort}", author,
		"{title}", title,
		"{series}", sanitiseValue(m.Series),
		"{series_index}", formatSeriesIndex(m.SeriesIndex),
		"{year}", yearOrEmpty(m.Year),
		"{id}", strconv.FormatInt(m.ID, 10),
	)
	return r.Replace(tpl)
}

func yearOrEmpty(y int) string {
	if y <= 0 {
		return ""
	}
	return strconv.Itoa(y)
}

// IsSafeRelative rejects anything that would escape the library root.
//
// Paths reach this from the database, which is mostly trustworthy, but a path
// traversal here would let a metadata edit write anywhere on the filesystem,
// so it is checked rather than assumed.
func IsSafeRelative(p string) bool {
	if p == "" || filepath.IsAbs(p) {
		return false
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	for _, r := range clean {
		if r == 0 || (unicode.IsControl(r) && r != '\t') {
			return false
		}
	}
	return true
}
