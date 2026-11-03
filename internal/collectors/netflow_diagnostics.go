package collectors

import (
	"log"
	"strings"
	"time"
)

// One shared budget per collector prevents changing malformed input or unknown
// template IDs from defeating per-reason rate limits. The UDP ingest loop is
// serial; HandleUDPFlow must likewise be called serially for its template cache.
type diagnosticWriter struct {
	logger     *log.Logger
	next       time.Time
	suppressed uint64
}

func (w *diagnosticWriter) Write(p []byte) (int, error) {
	now := time.Now()
	if now.Before(w.next) {
		w.suppressed++
		return len(p), nil
	}
	w.logger.Printf("[NETFLOW] %s (suppressed since last diagnostic: %d)", strings.TrimSpace(string(p)), w.suppressed)
	w.suppressed = 0
	w.next = now.Add(5 * time.Second)
	return len(p), nil
}

func (nf *Netflow) warnings() *log.Logger {
	if nf.warningLogger == nil {
		logger := nf.logger
		if logger == nil {
			logger = log.Default()
		}
		nf.warningLogger = log.New(&diagnosticWriter{logger: logger}, "", 0)
	}
	return nf.warningLogger
}
