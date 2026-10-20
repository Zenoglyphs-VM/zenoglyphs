package api

import (
	"log/slog"
	"net/http"
	"time"
)

// Access logging.
//
// Both servers ran silent until now: nothing recorded that a request had
// happened, so "the viewer is blank" and "the content origin is refusing
// everything" looked identical from the operator's side — no method, no path,
// no status, no latency, for anything.
//
// One line per response, from the logger the Server already carries.
//
// Two rules bound what goes in it, and both are about what an access log is
// NOT allowed to become:
//
//   - No headers, ever. The admin session cookie is the credential (see
//     admin.go), and a log that copies it turns log-read access into panel
//     access. The same argument the noStore comment makes about caching an
//     admin response applies to writing one down.
//   - No bodies. The login handler's body is the password.
//
// Query strings are logged. They carry filters — an owner address, a mime
// type, a limit — which are public data this API will hand to anyone who
// asks; no credential travels in one, because the panel authenticates with a
// cookie and a POST body.

// AccessLog wraps a handler chain with one log line per request.
//
// quiet is the content origin's posture. That origin serves raw bytes and
// every recursion fetch an inscribed page makes, so it sees far more requests
// than the API for far less information per request — a page composed of
// sixteen imports is seventeen lines saying the same thing. With quiet set,
// a successful response logs at debug (so `zcribe serve -v` still shows all
// of it) and only 4xx/5xx logs at info. The metadata API logs everything at
// info: its traffic is one line per human action.
func (s *Server) AccessLog(h http.Handler, origin string, quiet bool) http.Handler {
	if s.log == nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if quiet && rec.status < http.StatusBadRequest {
			level = slog.LevelDebug
		}
		if !s.log.Enabled(r.Context(), level) {
			return
		}
		attrs := []any{
			"origin", origin,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.written,
			// Milliseconds with one decimal: whole milliseconds round every
			// cache hit to 0, and a duration string sorts and aggregates
			// worse than a number.
			"ms", float64(time.Since(start).Microseconds()) / 1000,
			"ip", clientIP(r),
		}
		if r.URL.RawQuery != "" {
			attrs = append(attrs, "query", r.URL.RawQuery)
		}
		s.log.Log(r.Context(), level, "request", attrs...)
	})
}

// statusRecorder remembers what was actually sent.
//
// A handler that never calls WriteHeader still answers 200, which is why the
// zero value starts there rather than at 0. Byte counting is over what
// reached the socket, so a Range request logs the range it served and not the
// size of the inscription — http.ServeContent writes through Write.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	w.wrote = true
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, err
}

// Unwrap is what http.ResponseController follows to reach the real writer, so
// wrapping here does not cost a handler its flushing or hijacking.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
