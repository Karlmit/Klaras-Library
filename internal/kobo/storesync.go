package kobo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// storeSyncResult is what the real Kobo store contributed to a sync.
type storeSyncResult struct {
	Items       []json.RawMessage
	StoreToken  string // the store's own x-kobo-synctoken, to replay next time
	Continue    bool   // the store has more to send
	RecentReads string
	SyncMode    string
}

// storeSyncTimeout is deliberately well under the device's own ~30s limit.
//
// The store is a nice-to-have; the library is not. If Kobo is slow we drop it
// for this round rather than risk the device giving up on the whole request.
const storeSyncTimeout = 12 * time.Second

// syncWithStore forwards the sync to the real Kobo store and returns what it
// said.
//
// This is what makes KLARAS_KOBO_PROXY_STORE genuinely equivalent to
// calibre-web's proxy setting. Proxying only the endpoints we do not implement
// leaves /v1/library/sync entirely local, and a device that owns purchased Kobo
// books would watch them disappear.
//
// The store keeps its own sync position, which is why SyncToken carries
// raw_kobo_store_token: we hand the store back its own token and stash the new
// one inside ours, so both positions travel on the single header the device
// knows about.
func (h *Handler) syncWithStore(ctx context.Context, r *http.Request, tok *SyncToken) *storeSyncResult {
	if !h.proxyStore {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, storeSyncTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		koboStoreHost+"/v1/library/sync", nil)
	if err != nil {
		return nil
	}
	for k, vs := range r.Header {
		if isHopByHop(k) || strings.EqualFold(k, "Host") ||
			strings.EqualFold(k, SyncTokenHeader) {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	// The store must see its own position, not ours.
	if tok.RawKoboStoreToken != "" {
		req.Header.Set(SyncTokenHeader, tok.RawKoboStoreToken)
	}

	res, err := proxyClient.Do(req)
	if err != nil {
		h.log.Debug("kobo store sync unavailable; serving the library only", "err", err)
		return nil
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		h.log.Debug("kobo store sync returned an error", "status", res.StatusCode)
		return nil
	}

	var items []json.RawMessage
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&items); err != nil {
		h.log.Debug("could not parse the kobo store sync response", "err", err)
		return nil
	}

	return &storeSyncResult{
		Items:       items,
		StoreToken:  res.Header.Get(SyncTokenHeader),
		Continue:    res.Header.Get("x-kobo-sync") == "continue",
		RecentReads: res.Header.Get("x-kobo-recent-reads"),
		SyncMode:    res.Header.Get("x-kobo-sync-mode"),
	}
}

// apply folds the store's contribution into the response, bounded by limit.
//
// The cap matters as much as it does for our own entities. On a device that has
// never synced through us, raw_kobo_store_token is empty, so Kobo performs a
// FULL sync of the account and can return thousands of entitlements at once.
// Appending all of them produced a response far larger than the device expects
// -- and one it may take longer to read than its own timeout allows.
//
// Anything held back is not lost: the store token is only advanced when the
// whole batch was delivered, so the next request replays from the same point.
func (s *storeSyncResult) apply(items []any, tok *SyncToken, h http.Header, limit int) ([]any, bool) {
	if s == nil {
		return items, false
	}

	room := limit - len(items)
	if room < 0 {
		room = 0
	}
	deferred := false
	send := s.Items
	if len(send) > room {
		send, deferred = send[:room], true
	}
	for _, raw := range send {
		items = append(items, raw)
	}
	// Advance the store's position only when it answered AND we forwarded
	// everything it gave us. Advancing after a truncated batch would drop the
	// entitlements we held back.
	if s.StoreToken != "" && !deferred {
		tok.RawKoboStoreToken = s.StoreToken
	}
	if s.RecentReads != "" {
		setKoboHeader(h, "x-kobo-recent-reads", s.RecentReads)
	}
	if s.SyncMode != "" {
		setKoboHeader(h, "x-kobo-sync-mode", s.SyncMode)
	}
	// Ask the device to come back if the store has more, or if we truncated.
	return items, s.Continue || deferred
}
