package kepub

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// xmlish are the entries in an EPUB that a strict XML parser will read. Only
// these are touched: an image is full of bytes that would be illegal in XML and
// is none the worse for it.
func xmlish(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".opf", ".ncx", ".xhtml", ".html", ".htm", ".xml", ".smil":
		return true
	}
	return false
}

// stripIllegalXML removes the control characters XML 1.0 does not allow.
//
// Tab, newline and carriage return are the only control characters permitted;
// everything below 0x20 besides those is forbidden outright, and no parser will
// accept a document containing one. Books in this library do contain them --
// one had two NUL bytes padding the end of its content.opf, after </package>,
// where they change nothing about the meaning and stop the file being XML.
func stripIllegalXML(b []byte) ([]byte, int) {
	out := b[:0:0]
	removed := 0
	for _, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			removed++
			continue
		}
		out = append(out, c)
	}
	return out, removed
}

// repairXML writes a copy of an EPUB with the illegal characters removed from
// its XML, and reports how many it took out.
//
// A copy, never the original: the book on disk is the reader's, and this is a
// workaround for a converter's strictness rather than a defect worth rewriting
// someone's library over.
func repairXML(srcPath, dstPath string) (int, error) {
	zr, err := zip.OpenReader(srcPath)
	if err != nil {
		return 0, err
	}
	defer zr.Close()

	f, err := os.Create(dstPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	total := 0
	for _, e := range zr.File {
		rc, err := e.Open()
		if err != nil {
			return 0, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return 0, err
		}

		if xmlish(e.Name) {
			cleaned, n := stripIllegalXML(data)
			if n > 0 {
				data = cleaned
				total += n
			}
		}

		// The header is copied wholesale so mimetype keeps its stored,
		// uncompressed form -- an EPUB whose mimetype entry is deflated is not
		// a valid EPUB, and some readers do check.
		hdr := e.FileHeader
		hdr.UncompressedSize64 = uint64(len(data))
		hdr.UncompressedSize = 0
		hdr.CompressedSize64 = 0
		hdr.CompressedSize = 0
		hdr.CRC32 = 0
		w, err := zw.CreateHeader(&hdr)
		if err != nil {
			return 0, err
		}
		if _, err := w.Write(data); err != nil {
			return 0, err
		}
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, fmt.Errorf("nothing to repair")
	}
	return total, nil
}

// convertRepaired is the second attempt: strip the illegal characters into a
// scratch copy, convert that, and keep the result under the original's cache
// name so nothing downstream has to know a repair happened.
func (s *Service) convertRepaired(
	ctx context.Context, uuid, srcPath, out string,
) (string, int, bool) {
	scratch, err := os.CreateTemp(filepath.Dir(out), ".repair-*.epub")
	if err != nil {
		return "", 0, false
	}
	scratchName := scratch.Name()
	scratch.Close()
	defer os.Remove(scratchName)

	removed, err := repairXML(srcPath, scratchName)
	if err != nil || removed == 0 {
		return "", 0, false
	}

	zr, err := zip.OpenReader(scratchName)
	if err != nil {
		return "", 0, false
	}
	defer zr.Close()

	tmp, err := os.CreateTemp(filepath.Dir(out), ".tmp-*.kepub.epub")
	if err != nil {
		return "", 0, false
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := s.conv.Convert(ctx, tmp, zr); err != nil {
		tmp.Close()
		return "", 0, false
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", 0, false
	}
	if err := tmp.Close(); err != nil {
		return "", 0, false
	}
	if err := os.Rename(tmpName, out); err != nil {
		return "", 0, false
	}
	return out, removed, true
}
