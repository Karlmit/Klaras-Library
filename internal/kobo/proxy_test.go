package kobo

import (
	"net/http"
	"testing"
)

func TestCopyDecodedUpstreamHeadersLetsTransportManageCompression(t *testing.T) {
	src := http.Header{
		"Accept-Encoding":   {"gzip, deflate"},
		"Connection":        {"keep-alive"},
		"Host":              {"klaras.example"},
		SyncTokenHeader:     {"klaras-token"},
		"X-Kobo-UserKey":    {"user-key"},
		"X-Kobo-User-Agent": {"Kobo/1"},
	}
	dst := make(http.Header)
	copyDecodedUpstreamHeaders(dst, src, SyncTokenHeader)

	for _, name := range []string{"Accept-Encoding", "Connection", "Host", SyncTokenHeader} {
		if got := dst.Get(name); got != "" {
			t.Errorf("%s was forwarded as %q", name, got)
		}
	}
	if got := dst.Get("X-Kobo-UserKey"); got != "user-key" {
		t.Errorf("X-Kobo-UserKey = %q", got)
	}
	if got := dst.Get("X-Kobo-User-Agent"); got != "Kobo/1" {
		t.Errorf("X-Kobo-User-Agent = %q", got)
	}
}
