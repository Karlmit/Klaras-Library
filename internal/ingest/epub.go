// Package ingest imports ebook files that appear in the watch folder.
package ingest

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

// Metadata is what we can learn from an ebook file itself.
type Metadata struct {
	Title       string
	Authors     []string
	Series      string
	SeriesIndex *float64
	Description string
	Publisher   string
	Language    string
	PubDate     *time.Time
	Identifiers map[string]string
	CoverPath   string // path inside the zip, if a cover was found
}

// container locates the OPF package document inside an EPUB.
type container struct {
	Rootfiles struct {
		Rootfile []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfile"`
	} `xml:"rootfiles"`
}

// opfPackage is the subset of the OPF we read.
type opfPackage struct {
	Metadata struct {
		Title   []string `xml:"title"`
		Creator []struct {
			Name string `xml:",chardata"`
			Role string `xml:"role,attr"`
		} `xml:"creator"`
		Description []string `xml:"description"`
		Publisher   []string `xml:"publisher"`
		Language    []string `xml:"language"`
		Date        []string `xml:"date"`
		Identifier  []struct {
			Value  string `xml:",chardata"`
			Scheme string `xml:"scheme,attr"`
			ID     string `xml:"id,attr"`
		} `xml:"identifier"`
		Meta []struct {
			Name     string `xml:"name,attr"`
			Content  string `xml:"content,attr"`
			Property string `xml:"property,attr"`
			Value    string `xml:",chardata"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Item []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
}

// ReadEPUB extracts metadata from an EPUB without unpacking it.
func ReadEPUB(path string) (*Metadata, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("not a readable epub: %w", err)
	}
	defer zr.Close()

	opfPath, err := findOPF(&zr.Reader)
	if err != nil {
		return nil, err
	}
	f, err := zr.Open(opfPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", opfPath, err)
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil {
		return nil, err
	}
	var pkg opfPackage
	if err := xml.Unmarshal(raw, &pkg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", opfPath, err)
	}
	return fromOPF(&pkg, opfPath), nil
}

func findOPF(zr *zip.Reader) (string, error) {
	f, err := zr.Open("META-INF/container.xml")
	if err != nil {
		// Malformed but common: fall back to any .opf in the archive.
		for _, zf := range zr.File {
			if strings.HasSuffix(strings.ToLower(zf.Name), ".opf") {
				return zf.Name, nil
			}
		}
		return "", fmt.Errorf("no container.xml and no .opf found")
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return "", err
	}
	var c container
	if err := xml.Unmarshal(raw, &c); err != nil {
		return "", err
	}
	if len(c.Rootfiles.Rootfile) == 0 {
		return "", fmt.Errorf("container.xml lists no rootfile")
	}
	return c.Rootfiles.Rootfile[0].FullPath, nil
}

func fromOPF(pkg *opfPackage, opfPath string) *Metadata {
	m := &Metadata{Identifiers: map[string]string{}}

	if len(pkg.Metadata.Title) > 0 {
		m.Title = strings.TrimSpace(pkg.Metadata.Title[0])
	}
	for _, c := range pkg.Metadata.Creator {
		if n := strings.TrimSpace(c.Name); n != "" {
			m.Authors = append(m.Authors, n)
		}
	}
	if len(pkg.Metadata.Description) > 0 {
		m.Description = strings.TrimSpace(pkg.Metadata.Description[0])
	}
	if len(pkg.Metadata.Publisher) > 0 {
		m.Publisher = strings.TrimSpace(pkg.Metadata.Publisher[0])
	}
	if len(pkg.Metadata.Language) > 0 {
		m.Language = normaliseLang(strings.TrimSpace(pkg.Metadata.Language[0]))
	}
	for _, d := range pkg.Metadata.Date {
		if t, ok := parseFlexibleDate(strings.TrimSpace(d)); ok {
			m.PubDate = &t
			break
		}
	}
	for _, id := range pkg.Metadata.Identifier {
		v := strings.TrimSpace(id.Value)
		if v == "" {
			continue
		}
		scheme := strings.ToLower(strings.TrimSpace(id.Scheme))
		if scheme == "" {
			// EPUB3 puts the scheme in the value: "urn:isbn:978...".
			if strings.HasPrefix(strings.ToLower(v), "urn:isbn:") {
				scheme, v = "isbn", v[len("urn:isbn:"):]
			} else if strings.HasPrefix(strings.ToLower(v), "urn:uuid:") {
				continue // the book's own id, not an external identifier
			}
		}
		if scheme != "" {
			m.Identifiers[scheme] = v
		}
	}

	// Calibre writes series into <meta name="calibre:series">, which is how
	// almost every Swedish ebook in this library carries its series.
	for _, meta := range pkg.Metadata.Meta {
		switch {
		case meta.Name == "calibre:series":
			m.Series = strings.TrimSpace(meta.Content)
		case meta.Name == "calibre:series_index":
			if f, ok := parseFloat(meta.Content); ok {
				m.SeriesIndex = &f
			}
		case strings.HasSuffix(meta.Property, "belongs-to-collection"):
			if m.Series == "" {
				m.Series = strings.TrimSpace(meta.Value)
			}
		}
	}

	// Locate a cover image: EPUB3 marks it with properties="cover-image",
	// EPUB2 points at it with <meta name="cover" content="item-id">.
	base := path.Dir(opfPath)
	var coverID string
	for _, meta := range pkg.Metadata.Meta {
		if meta.Name == "cover" {
			coverID = meta.Content
		}
	}
	for _, item := range pkg.Manifest.Item {
		if strings.Contains(item.Properties, "cover-image") ||
			(coverID != "" && item.ID == coverID) {
			m.CoverPath = path.Join(base, item.Href)
			break
		}
	}
	return m
}

// normaliseLang converts a BCP-47 tag to the three-letter code used elsewhere.
func normaliseLang(s string) string {
	s = strings.ToLower(s)
	if i := strings.IndexAny(s, "-_"); i > 0 {
		s = s[:i]
	}
	two := map[string]string{
		"sv": "swe", "en": "eng", "da": "dan", "no": "nor", "nb": "nor",
		"de": "deu", "fr": "fra", "es": "spa", "fi": "fin", "nl": "nld",
		"it": "ita", "ar": "ara", "pl": "pol", "ru": "rus",
	}
	if v, ok := two[s]; ok {
		return v
	}
	return s
}

func parseFlexibleDate(s string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05", "2006-01-02", "2006-01", "2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			// Guard against placeholder years, as Calibre's 0101 sentinel.
			if t.Year() > 102 {
				return t.UTC(), true
			}
		}
	}
	return time.Time{}, false
}

func parseFloat(s string) (float64, bool) {
	var f float64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &f); err != nil {
		return 0, false
	}
	return f, true
}

// ExtractCover pulls the cover image out of an EPUB.
func ExtractCover(epubPath, coverInZip string, w io.Writer) error {
	if coverInZip == "" {
		return fmt.Errorf("no cover in this epub")
	}
	zr, err := zip.OpenReader(epubPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	f, err := zr.Open(coverInZip)
	if err != nil {
		return fmt.Errorf("cover %s not in archive: %w", coverInZip, err)
	}
	defer f.Close()
	_, err = io.Copy(w, io.LimitReader(f, 32<<20))
	return err
}
