package library

import (
	"strconv"
	"strings"
	"testing"
)

// The number in these titles is the only record of where a book sits in its
// series, so reading it out has to be right. Getting it wrong scrambles the
// order of a forty-book series, and the evidence is gone once the title is
// rewritten.
func TestSeriesTitleParsing(t *testing.T) {
	cases := []struct {
		title  string
		prefix string
		index  string
		sub    string
	}{
		{"Isfolket 2 - Häxjakten", "Isfolket", "2", "Häxjakten"},
		{"Sagan om Isfolket 1 - Trollbunden", "Isfolket", "1", "Trollbunden"},
		// No subtitle: still numbered, and the title must survive intact.
		{"Isfolket 39", "Isfolket", "39", ""},
		// The dashes a Swedish library actually contains.
		{"Isfolket 12 – Feber i blodet", "Isfolket", "12", "Feber i blodet"},
		{"Isfolket 12 — Feber i blodet", "Isfolket", "12", "Feber i blodet"},
		{"Isfolket 12: Feber i blodet", "Isfolket", "12", "Feber i blodet"},
		// Half numbers exist in series: Mistborn #2.5 and friends.
		{"Mistborn 2.5 - Secret History", "Mistborn", "2.5", "Secret History"},
		// A multi-word series name.
		{"De svarta riddarna 2 - Dit ingen går", "De svarta riddarna", "2", "Dit ingen går"},
		// The number must be the series number, not one inside the subtitle.
		{"Isfolket 30 - Människodjuret", "Isfolket", "30", "Människodjuret"},
	}
	for _, c := range cases {
		m := seriesTitle.FindStringSubmatch(c.title)
		if m == nil {
			t.Errorf("%q did not parse at all", c.title)
			continue
		}
		gotPrefix := strings.TrimSpace(m[1])
		gotSub := strings.TrimSpace(m[3])
		if gotPrefix != c.prefix || m[2] != c.index || gotSub != c.sub {
			t.Errorf("\n %q\n got  prefix=%q index=%q sub=%q\n want prefix=%q index=%q sub=%q",
				c.title, gotPrefix, m[2], gotSub, c.prefix, c.index, c.sub)
		}
		if _, err := strconv.ParseFloat(m[2], 64); err != nil {
			t.Errorf("%q: index %q is not a number", c.title, m[2])
		}
	}
}

// Titles that merely contain a number are not series entries, and treating them
// as such would rename ordinary books and move their files.
func TestSeriesTitleLeavesOrdinaryTitlesAlone(t *testing.T) {
	for _, title := range []string{
		"Elara och isfolket",
		"Isfolket",
		"1984",
		"Trollbunden",
	} {
		m := seriesTitle.FindStringSubmatch(title)
		if m != nil && m[3] != "" {
			t.Errorf("%q should not look like a numbered series entry, got prefix=%q index=%q",
				title, m[1], m[2])
		}
	}
}
