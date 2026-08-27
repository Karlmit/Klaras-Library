package kepub

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestStripIllegalXML(t *testing.T) {
	// The case from the library: NUL padding after the closing tag.
	got, n := stripIllegalXML([]byte("<package>x</package>\n\x00\x00"))
	if string(got) != "<package>x</package>\n" || n != 2 {
		t.Errorf("got %q removing %d", got, n)
	}

	// Tab, newline and carriage return are legal and must survive: stripping
	// them would reflow every document in the book.
	keep := []byte("a\tb\nc\r\nd")
	got, n = stripIllegalXML(keep)
	if string(got) != string(keep) || n != 0 {
		t.Errorf("whitespace must be kept, got %q removing %d", got, n)
	}

	// Text with nothing wrong comes back untouched.
	if got, n := stripIllegalXML([]byte("<p>Hemsöborna</p>")); n != 0 || string(got) != "<p>Hemsöborna</p>" {
		t.Errorf("clean input changed: %q, %d", got, n)
	}

	// The other forbidden ranges, including the vertical tab and form feed
	// that sit between the legal ones.
	if _, n := stripIllegalXML([]byte("a\x01b\x08c\x0bd\x0ce\x0ef\x1fg")); n != 6 {
		t.Errorf("expected 6 removals, got %d", n)
	}
}

func TestXmlish(t *testing.T) {
	for _, n := range []string{"content.opf", "toc.ncx", "OPS/ch1.xhtml", "a.HTML", "x.xml"} {
		if !xmlish(n) {
			t.Errorf("%s should be treated as XML", n)
		}
	}
	// Binary entries are full of bytes XML forbids and must never be rewritten.
	for _, n := range []string{"cover.jpg", "font.otf", "mimetype", "style.css", "a.png"} {
		if xmlish(n) {
			t.Errorf("%s must be left alone", n)
		}
	}
}

// repairXML has to produce a file that is still a valid EPUB: same entries, in
// the same order, with mimetype first and uncompressed, and every binary entry
// byte-for-byte what it was.
func TestRepairXMLKeepsTheBookIntact(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.epub")
	image := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}

	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	mt, _ := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	mt.Write([]byte("application/epub+zip"))
	opf, _ := zw.CreateHeader(&zip.FileHeader{Name: "content.opf", Method: zip.Deflate})
	opf.Write([]byte("<package>ok</package>\x00\x00"))
	img, _ := zw.CreateHeader(&zip.FileHeader{Name: "cover.jpg", Method: zip.Deflate})
	img.Write(image)
	zw.Close()
	f.Close()

	dst := filepath.Join(dir, "out.epub")
	n, err := repairXML(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 characters removed, got %d", n)
	}

	zr, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	if len(zr.File) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(zr.File))
	}
	if zr.File[0].Name != "mimetype" {
		t.Errorf("mimetype must stay first, got %s", zr.File[0].Name)
	}
	if zr.File[0].Method != zip.Store {
		t.Error("mimetype must stay uncompressed, or the result is not a valid EPUB")
	}

	read := func(name string) []byte {
		for _, e := range zr.File {
			if e.Name == name {
				rc, err := e.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer rc.Close()
				b := make([]byte, e.UncompressedSize64)
				_, _ = rc.Read(b)
				return b
			}
		}
		t.Fatalf("%s missing from the repaired copy", name)
		return nil
	}

	if got := string(read("content.opf")); got != "<package>ok</package>" {
		t.Errorf("opf is %q", got)
	}
	if got := read("cover.jpg"); string(got) != string(image) {
		t.Errorf("the image was altered: %v", got)
	}

	// A book with nothing wrong should report so rather than be rewritten for
	// no reason.
	clean := filepath.Join(dir, "clean.epub")
	cf, _ := os.Create(clean)
	cw := zip.NewWriter(cf)
	w, _ := cw.Create("content.opf")
	w.Write([]byte("<package>fine</package>"))
	cw.Close()
	cf.Close()
	if _, err := repairXML(clean, filepath.Join(dir, "clean-out.epub")); err == nil {
		t.Error("a book needing no repair should say so")
	}
}
