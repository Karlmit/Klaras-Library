package provider

import "testing"

// A library catalogue writes names for shelving, not for reading. Left as-is
// they would go into the file tree as "Strindberg, August, 1849-1912".
func TestLibrisName(t *testing.T) {
	cases := map[string]string{
		"Strindberg, August, 1849-1912": "August Strindberg",
		"Strindberg, August":            "August Strindberg",
		"Lagerlöf, Selma, 1858-1940":    "Selma Lagerlöf",
		"August Strindberg":             "August Strindberg",
		"Strindberg,":                   "Strindberg",
		// A comma that is part of the name, not a life date, must survive.
		"Smith, John Paul": "John Paul Smith",
		"":                 "",
	}
	for in, want := range cases {
		if got := librisName(in); got != want {
			t.Errorf("librisName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The date field arrives in whatever shape the cataloguer typed.
func TestLibrisYear(t *testing.T) {
	cases := []struct {
		in   librisValue
		want string
	}{
		{librisValue{"2007"}, "2007"},
		{librisValue{"[2025]", "2025"}, "2025"},
		{librisValue{"cop. 1998"}, "1998"},
		{librisValue{"1849-1912"}, "1849"},
		{librisValue{"n.d."}, ""},
		{librisValue{}, ""},
		// Three digits then a break must not be read as a year.
		{librisValue{"199-?"}, ""},
	}
	for _, c := range cases {
		if got := librisYear(c.in); got != c.want {
			t.Errorf("librisYear(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Libris returns a bare string when a record has one value and an array when
// it has several. Decoding only one shape drops half the catalogue.
func TestLibrisValueBothShapes(t *testing.T) {
	var one, many librisValue
	if err := one.UnmarshalJSON([]byte(`"2007"`)); err != nil {
		t.Fatal(err)
	}
	if err := many.UnmarshalJSON([]byte(`["[2025]","2025"]`)); err != nil {
		t.Fatal(err)
	}
	if one.first() != "2007" || many.first() != "[2025]" {
		t.Errorf("got %v and %v", one, many)
	}
	if len(many) != 2 {
		t.Errorf("array form should keep every value, got %d", len(many))
	}
}
