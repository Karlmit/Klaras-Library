package provider

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type stub struct {
	name string
	res  []Result
	err  error
}

func (s stub) Name() string { return s.name }
func (s stub) Search(context.Context, Query, int) ([]Result, error) {
	return s.res, s.err
}

// Search feeds a JSON response directly, where a nil slice marshals to `null`
// rather than `[]`. A client reading .length off that crashes, and the crash
// takes out the whole page -- so the empty case is worth pinning down.
func TestSearchNeverReturnsNil(t *testing.T) {
	cases := map[string]*Set{
		"no providers at all": NewSetOf(),
		"provider found none": NewSetOf(stub{name: "empty"}),
		"every provider errored": NewSetOf(
			stub{name: "a", err: errors.New("timeout")},
			stub{name: "b", err: errors.New("503")},
		),
	}
	for name, set := range cases {
		got := set.Search(context.Background(), Query{Title: "whatever"}, 10)
		if got == nil {
			t.Errorf("%s: returned a nil slice", name)
			continue
		}
		if len(got) != 0 {
			t.Errorf("%s: expected no results, got %d", name, len(got))
		}
		b, err := json.Marshal(map[string]any{"results": got})
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if string(b) != `{"results":[]}` {
			t.Errorf("%s: serialises as %s, want {\"results\":[]}", name, b)
		}
	}
}

// The happy path still has to work: results from several providers come back
// together, and the limit is respected.
func TestSearchCombinesProviders(t *testing.T) {
	set := NewSetOf(
		stub{name: "a", res: []Result{{Title: "One"}, {Title: "Two"}}},
		stub{name: "b", res: []Result{{Title: "Three"}}},
		stub{name: "c", err: errors.New("down")},
	)
	got := set.Search(context.Background(), Query{Title: "One"}, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 results from the two working providers, got %d", len(got))
	}
	if got := set.Search(context.Background(), Query{Title: "One"}, 2); len(got) != 2 {
		t.Errorf("limit not applied: got %d results, want 2", len(got))
	}
}
