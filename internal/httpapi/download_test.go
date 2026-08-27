package httpapi

import "testing"

// A Kobo only treats a sideloaded file as a KEPUB if it ends .kepub.epub, so
// this name is not cosmetic: get it wrong and the book loads as a plain EPUB,
// losing the page turns and progress the conversion was for.
func TestKepubDownloadName(t *testing.T) {
	cases := map[string]string{
		// How Calibre stores them, which is most of this library.
		"Uppdrag Hail Mary - Andy Weir.kepub": "Uppdrag Hail Mary - Andy Weir.kepub.epub",
		// How a converted one arrives.
		"Hemsoborna - August Strindberg.epub": "Hemsoborna - August Strindberg.kepub.epub",
		// Already correct: must not become .kepub.kepub.epub.
		"Book.kepub.epub": "Book.kepub.epub",
		// A dot in the title is not an extension to strip twice.
		"Vol. 2 - Someone.kepub":                           "Vol. 2 - Someone.kepub.epub",
		"#tillsammans #utanför - gunnarsson, Camilla.epub": "#tillsammans #utanför - gunnarsson, Camilla.kepub.epub",
	}
	for in, want := range cases {
		if got := kepubDownloadName(in); got != want {
			t.Errorf("\n in   %q\n got  %q\n want %q", in, got, want)
		}
	}
}
