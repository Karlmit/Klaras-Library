package kobo

import (
	"strings"

	"golang.org/x/text/language"
)

// bibliographicToTerminological maps the ISO 639-2/B codes to their 639-2/T
// equivalents.
//
// ISO 639-2 has two codes for twenty languages: a bibliographic one derived
// from the English name and a terminological one derived from the native name
// -- "ger" and "deu" for German, "fre" and "fra" for French. Calibre normally
// writes the terminological form, which x/text resolves on its own, but a book
// imported from elsewhere can carry either. Without this the bibliographic
// codes fall through untranslated and the device sees a three-letter code.
var bibliographicToTerminological = map[string]string{
	"alb": "sqi", "arm": "hye", "baq": "eus", "bur": "mya", "chi": "zho",
	"cze": "ces", "dut": "nld", "fre": "fra", "geo": "kat", "ger": "deu",
	"gre": "ell", "ice": "isl", "mac": "mkd", "mao": "mri", "may": "msa",
	"per": "fas", "rum": "ron", "slo": "slk", "tib": "bod", "wel": "cym",
}

// koboLanguage renders a language code the way a Kobo expects it.
//
// Calibre stores three-letter ISO 639-2 codes -- "swe" for Swedish -- and the
// device wants the two-letter ISO 639-1 form, "sv". calibre-web converts;
// this did not, and sent "swe" straight through. That is not a cosmetic
// difference in a library that is 94% Swedish: the code appears on every
// entitlement the device is offered.
//
// Anything that cannot be resolved to a two-letter code falls back to "en",
// which is what calibre-web does with a book that has no language at all. A
// wrong-but-valid code is handled by the device; an unparseable one may not be.
func koboLanguage(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return "en"
	}
	// Calibre occasionally stores a full tag such as "pt-BR"; the device wants
	// just the base language.
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	if t, ok := bibliographicToTerminological[code]; ok {
		code = t
	}
	base, err := language.ParseBase(code)
	if err != nil {
		return "en"
	}
	// ParseBase echoes codes it does not recognise, including the ISO 639-2
	// codes with no 639-1 equivalent at all.
	if s := base.String(); len(s) == 2 {
		return s
	}
	return "en"
}

// koboFormatNames gives the names a stored format is advertised under.
//
// This mirrors calibre-web's KOBO_FORMATS. An EPUB goes out as both EPUB3 and
// EPUB because the device's accepted-format list has varied across firmware
// versions, and offering both costs one extra entry.
func koboFormatNames(format string) []string {
	switch strings.ToUpper(format) {
	case "KEPUB":
		return []string{"KEPUB"}
	case "EPUB":
		return []string{"EPUB3", "EPUB"}
	default:
		return []string{strings.ToUpper(format)}
	}
}
