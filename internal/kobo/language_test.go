package kobo

import "testing"

// TestKoboLanguage covers the codes this library actually contains, plus the
// shapes that broke it.
//
// Calibre stores ISO 639-2; the device wants ISO 639-1. Sending "swe" where
// "sv" belongs put a code the device does not recognise on 94% of the
// entitlements offered here.
func TestKoboLanguage(t *testing.T) {
	cases := map[string]string{
		// Everything present in the real library.
		"swe": "sv", "eng": "en", "dan": "da", "ara": "ar",
		"nor": "no", "deu": "de", "fra": "fr", "swa": "sw",

		// Bibliographic variants, which x/text does not resolve by itself.
		"ger": "de", "fre": "fr", "dut": "nl", "cze": "cs", "gre": "el",

		// Already correct, or nearly so.
		"sv": "sv", "en": "en", "SWE": "sv", " swe ": "sv",
		"pt-BR": "pt", "en_GB": "en",

		// No language, or nothing usable: calibre-web's fallback.
		"":    "en",
		"zxx": "en", // "no linguistic content" -- no 639-1 code exists
		"und": "en",
		"???": "en",
	}
	for in, want := range cases {
		if got := koboLanguage(in); got != want {
			t.Errorf("koboLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestKoboLanguageIsAlwaysTwoLetters is the property that matters: whatever
// arrives, the device is never handed a three-letter code.
func TestKoboLanguageIsAlwaysTwoLetters(t *testing.T) {
	for _, in := range []string{
		"swe", "eng", "qqq", "", "x", "toolongcode", "zzz", "mul", "nob", "sgn",
		"1234", "sv-SE", "zh-Hant", "  ", "EN",
	} {
		if got := koboLanguage(in); len(got) != 2 {
			t.Errorf("koboLanguage(%q) = %q, which is not a two-letter code", in, got)
		}
	}
}

// TestKoboFormatNames mirrors calibre-web's KOBO_FORMATS.
func TestKoboFormatNames(t *testing.T) {
	cases := map[string][]string{
		"EPUB":  {"EPUB3", "EPUB"},
		"epub":  {"EPUB3", "EPUB"},
		"KEPUB": {"KEPUB"},
		"kepub": {"KEPUB"},
		"PDF":   {"PDF"},
	}
	for in, want := range cases {
		got := koboFormatNames(in)
		if len(got) != len(want) {
			t.Errorf("koboFormatNames(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("koboFormatNames(%q) = %v, want %v", in, got, want)
				break
			}
		}
	}
}
