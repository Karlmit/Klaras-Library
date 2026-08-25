package provider

import (
	"sort"
	"strings"
	"unicode"
)

// scoreAll rates each candidate against what we already know.
//
// Providers return loosely-matching results, and for a Swedish library the top
// Google Books hit is often an English edition of a different book. Scoring
// locally lets the UI put the plausible ones first rather than trusting
// whichever provider answered fastest.
func scoreAll(rs []Result, q Query) {
	wantTitle := fold(q.Title)
	wantAuthor := fold(q.Author)

	for i := range rs {
		r := &rs[i]
		s := 0.0

		if wantTitle != "" {
			s += 3 * similarity(fold(r.Title), wantTitle)
		}
		if wantAuthor != "" && len(r.Authors) > 0 {
			best := 0.0
			for _, a := range r.Authors {
				if v := similarity(fold(a), wantAuthor); v > best {
					best = v
				}
			}
			s += 2 * best
		}
		// An exact ISBN match is far stronger evidence than any name similarity.
		if q.ISBN != "" && r.Identifiers["isbn"] != "" &&
			normaliseISBN(r.Identifiers["isbn"]) == normaliseISBN(q.ISBN) {
			s += 10
		}
		// Prefer a result in the language the library is actually in.
		if q.Lang != "" && r.Language != "" && strings.EqualFold(r.Language, q.Lang) {
			s += 1
		}
		// Richer records are more useful to apply.
		if r.Description != "" {
			s += 0.4
		}
		if r.CoverURL != "" {
			s += 0.4
		}
		if r.Series != "" {
			s += 0.2
		}
		r.Score = s
	}
}

func sortByScore(rs []Result) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Score > rs[j].Score })
}

// fold lowercases and strips accents and punctuation so "Röda Rummet" and
// "roda rummet" compare equal.
func fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case 'å', 'ä', 'æ':
			b.WriteRune('a')
		case 'ö', 'ø', 'ô':
			b.WriteRune('o')
		case 'é', 'è', 'ê':
			b.WriteRune('e')
		case 'ü':
			b.WriteRune('u')
		default:
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			} else if unicode.IsSpace(r) {
				b.WriteRune(' ')
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// similarity is Dice coefficient over character bigrams: cheap, and forgiving
// of word order and small differences in a way that exact matching is not.
func similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ab, bb := bigrams(a), bigrams(b)
	if len(ab) == 0 || len(bb) == 0 {
		return 0
	}
	hits := 0
	for g, n := range ab {
		if m, ok := bb[g]; ok {
			hits += min(n, m)
		}
	}
	return 2 * float64(hits) / float64(countAll(ab)+countAll(bb))
}

func bigrams(s string) map[string]int {
	r := []rune(s)
	out := map[string]int{}
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])]++
	}
	return out
}

func countAll(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func normaliseISBN(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) || r == 'X' || r == 'x' {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}
