package httpapi

import (
	"fmt"
	"net/http"
	"time"
)

type logSnapshotter interface {
	Snapshot() []byte
}

// handleDownloadLogs gives an administrator the recent in-process log buffer.
// Docker's older logs remain available through docker logs; this deliberately
// stays bounded and begins at the current process start.
func (s *Server) handleDownloadLogs(w http.ResponseWriter, _ *http.Request) {
	if s.logs == nil {
		writeErr(w, http.StatusServiceUnavailable, "log download is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(
		`attachment; filename="klaras-logs-%s.txt"`, time.Now().UTC().Format("20060102-150405")))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.logs.Snapshot())
}
