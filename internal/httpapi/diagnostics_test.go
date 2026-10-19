package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticLogs string

func (s staticLogs) Snapshot() []byte { return []byte(s) }

func TestDownloadLogs(t *testing.T) {
	s := &Server{logs: staticLogs("one\ntwo\n")}
	rec := httptest.NewRecorder()
	s.handleDownloadLogs(rec, httptest.NewRequest(http.MethodGet, "/api/diagnostics/logs", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "one\ntwo\n" {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Errorf("Content-Disposition = %q", got)
	}
}

func TestSafeRequestPathRedactsKoboToken(t *testing.T) {
	cases := map[string]string{
		"/api/books":                   "/api/books",
		"/kobo/secret/v1/sync":         "/kobo/[redacted]/v1/sync",
		"/prefix/kobo/secret":          "/prefix/kobo/[redacted]",
		"/kobo/secret/download/1/epub": "/kobo/[redacted]/download/1/epub",
	}
	for in, want := range cases {
		if got := safeRequestPath(in); got != want {
			t.Errorf("safeRequestPath(%q) = %q, want %q", in, got, want)
		}
	}
}
