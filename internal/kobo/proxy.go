package kobo

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// koboStoreHost is the real Kobo API.
const koboStoreHost = "https://storeapi.kobo.com"

// proxyClient has a short timeout: a slow Kobo store must never hold up a
// device request long enough to trip its own ~30s limit.
var proxyClient = &http.Client{Timeout: 10 * time.Second}

// hopByHopHeaders must not be forwarded through a proxy.
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailers", "Transfer-Encoding", "Upgrade",
}

// proxyToKoboStore forwards an unimplemented request to the real Kobo store.
//
// This is what keeps the device's own shop, recommendations and account screens
// working while its library points here. It is optional, and off by default:
// with it on, the device talks to Kobo about everything except the library.
func (h *Handler) proxyToKoboStore(w http.ResponseWriter, r *http.Request) {
	// Strip the /kobo/{token} prefix so the store sees the path it expects, and
	// never forward our own auth token upstream.
	//
	// GET is redirected rather than fetched. calibre-web does the same, and the
	// difference matters: fetching means the store sees OUR request, without the
	// device's own Kobo credentials, so account endpoints come back
	// unauthenticated and the device is told it has no profile, no subscriptions
	// and no benefits right before it starts a sync. A 307 lets the device talk
	// to Kobo itself, as it did before its library moved here, and keeps its
	// session where the device can actually use it. Writes are still forwarded:
	// a redirected POST would have to replay a body we have already consumed.
	path := r.URL.Path
	if i := strings.Index(path, "/v1/"); i >= 0 {
		path = path[i:]
	} else {
		h.emptyOK(w)
		return
	}

	target, err := url.Parse(koboStoreHost + path)
	if err != nil {
		h.emptyOK(w)
		return
	}
	target.RawQuery = r.URL.RawQuery

	if r.Method == http.MethodGet {
		http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(),
		io.LimitReader(r.Body, 4<<20))
	if err != nil {
		h.emptyOK(w)
		return
	}
	for k, vs := range r.Header {
		if isHopByHop(k) || strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := proxyClient.Do(req)
	if err != nil {
		// The store being unreachable must not become a device-visible error.
		h.log.Debug("kobo store proxy failed", "path", path, "err", err)
		h.emptyOK(w)
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 32<<20))
}

func (h *Handler) emptyOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

func isHopByHop(h string) bool {
	for _, x := range hopByHopHeaders {
		if strings.EqualFold(h, x) {
			return true
		}
	}
	return false
}
