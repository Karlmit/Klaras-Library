package kobo

import (
	"encoding/json"
	"net/http"
)

// The helpers below expose the store-merge internals to the package's external
// tests. Kept in their own file so the production path stays unexported.

// NewStoreResultForTest builds a storeSyncResult.
func NewStoreResultForTest(items []json.RawMessage, token string, cont bool) any {
	for i := range items {
		if items[i] == nil {
			items[i] = json.RawMessage(`{"NewEntitlement":{}}`)
		}
	}
	return &storeSyncResult{Items: items, StoreToken: token, Continue: cont}
}

// ApplyStoreResultForTest runs the merge.
func ApplyStoreResultForTest(s any, items []any, tok *SyncToken, h http.Header, limit int) ([]any, bool) {
	r, _ := s.(*storeSyncResult)
	return r.apply(items, tok, h, limit)
}
