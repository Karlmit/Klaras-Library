package library

import "strings"

// Every keyset template is substituted by simple string replacement, so no
// placeholder may be a prefix of another. $CUR and $CURID once were, which
// turned "$CURID" into "$13ID" and broke every page after the first.
func init() {
	for mode, spec := range sortSpecs {
		if strings.Contains(spec.keyset, "$CUR") {
			panic("sort " + string(mode) + " still uses the old $CUR placeholder")
		}
		if !strings.Contains(spec.keyset, "{{key}}") || !strings.Contains(spec.keyset, "{{id}}") {
			panic("sort " + string(mode) + " is missing a keyset placeholder")
		}
	}
	// The two placeholders must remain mutually non-prefixing.
	if strings.HasPrefix("{{id}}", "{{key}}") || strings.HasPrefix("{{key}}", "{{id}}") {
		panic("keyset placeholders prefix each other; substitution will corrupt them")
	}
}
