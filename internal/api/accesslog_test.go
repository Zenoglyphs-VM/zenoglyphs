package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func logged(t *testing.T, quiet bool, level slog.Level, method, target string) string {
	t.Helper()
	s, st := newServer(t)
	seed(t, st)

	var buf bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level}))

	h := s.AccessLog(s.Routes(nil), "api", quiet)
	do(t, h, method, target, map[string]string{"Cookie": "zenoglyphs_admin=super-secret-session"})
	return buf.String()
}

func TestAccessLogRecordsTheRequest(t *testing.T) {
	out := logged(t, false, slog.LevelInfo, http.MethodGet, "/assets?owner=z1probe")
	for _, want := range []string{
		`method=GET`, `path=/assets`, `status=200`, `query="owner=z1probe"`, `origin=api`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("access log is missing %s:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "ms=") || !strings.Contains(out, "bytes=") {
		t.Errorf("access log carries no latency or size:\n%s", out)
	}
}

// The admin session cookie is the credential. A log line that copies it turns
// read access to the logs into access to the panel — the same argument
// admin.go's noStore makes about caching an admin response.
func TestAccessLogNeverCarriesCredentials(t *testing.T) {
	out := logged(t, false, slog.LevelInfo, http.MethodGet, "/info")
	if strings.Contains(out, "super-secret-session") || strings.Contains(strings.ToLower(out), "cookie") {
		t.Fatalf("access log leaked a request header:\n%s", out)
	}
}

// The content origin answers every recursion fetch, so its successes are
// debug and only its failures are info. Without that, one composite page
// costs seventeen log lines and buries the API it sits beside.
func TestQuietAccessLogKeepsSuccessesOutOfInfo(t *testing.T) {
	if out := logged(t, true, slog.LevelInfo, http.MethodGet, "/info"); out != "" {
		t.Errorf("quiet origin logged a 200 at info level:\n%s", out)
	}
	if out := logged(t, true, slog.LevelDebug, http.MethodGet, "/info"); !strings.Contains(out, "status=200") {
		t.Errorf("-v should still show a quiet origin's successes:\n%s", out)
	}
	out := logged(t, true, slog.LevelInfo, http.MethodGet, "/asset/does-not-exist")
	if !strings.Contains(out, "status=404") {
		t.Errorf("a quiet origin must still report failures at info:\n%s", out)
	}
}
