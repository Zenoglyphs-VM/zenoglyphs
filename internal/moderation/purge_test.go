package moderation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseBlocklist(t *testing.T) {
	in := strings.NewReader(`# a comment line, ignored

aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900
bb11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900 # csam report #4471
  cc11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900
`)
	got, err := ParseBlocklist(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(got), got)
	}
	if got[0].ID != "aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900" || got[0].Reason != "" {
		t.Errorf("entry 0 = %+v", got[0])
	}
	if got[1].Reason != "csam report #4471" {
		t.Errorf("entry 1 reason = %q", got[1].Reason)
	}
	if got[2].ID != "cc11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900" {
		t.Errorf("entry 2 id not trimmed: %+v", got[2])
	}
}

func TestParseBlocklistRejectsBadID(t *testing.T) {
	for _, in := range []string{"not-hex-and-too-short", "zz11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900"} {
		if _, err := ParseBlocklist(strings.NewReader(in)); err == nil {
			t.Errorf("ParseBlocklist(%q): want error, got nil", in)
		}
	}
}

func TestWriteBlocklistRoundTrips(t *testing.T) {
	entries := []Entry{
		{ID: "aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900", Reason: "spam"},
		{ID: "bb11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900"},
	}
	var buf strings.Builder
	WriteBlocklist(&buf, entries)

	got, err := ParseBlocklist(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Reason != "spam" || got[1].Reason != "" {
		t.Errorf("round trip = %+v", got)
	}
}

func TestURLsFor(t *testing.T) {
	got := URLsFor("https://content.example.org/", []string{"abc123"})
	want := []string{
		"https://content.example.org/content/abc123/",
		"https://content.example.org/content/abc123",
		"https://content.example.org/r/asset/abc123",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("url %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestConfiguredFalseByDefault(t *testing.T) {
	t.Setenv("ZCRIBE_CF_ZONE", "")
	t.Setenv("ZCRIBE_CF_TOKEN", "")
	t.Setenv("ZCRIBE_PURGE_WEBHOOK", "")
	if Configured() {
		t.Error("Configured() = true with no env vars set")
	}
	t.Setenv("ZCRIBE_PURGE_WEBHOOK", "https://example.org/purge")
	if !Configured() {
		t.Error("Configured() = false with ZCRIBE_PURGE_WEBHOOK set")
	}
}

// The Cloudflare path is not exercised here — its endpoint is a hardcoded
// api.cloudflare.com URL, unmockable without changing the function signature,
// same as the existing (also untested) per-id purgeCloudflare. The webhook
// path takes its URL as configuration, so it is the one this package can
// actually verify end to end.
func TestPurgeAllWebhookSendsPurgeAllFlagNotURLs(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	t.Setenv("ZCRIBE_CF_ZONE", "")
	t.Setenv("ZCRIBE_CF_TOKEN", "")
	t.Setenv("ZCRIBE_PURGE_WEBHOOK", srv.URL)
	t.Setenv("ZCRIBE_PURGE_TOKEN", "shh")

	backend, err := PurgeAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if backend != "webhook" {
		t.Errorf("backend = %q, want webhook", backend)
	}
	if gotAuth != "Bearer shh" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	// purgeAll:true and no urls key — a receiver keyed on len(urls)==0 would
	// otherwise read this as "purge nothing" instead of "purge everything".
	if v, _ := gotBody["purgeAll"].(bool); !v {
		t.Errorf("body = %v, want purgeAll:true", gotBody)
	}
	if _, has := gotBody["urls"]; has {
		t.Errorf("body = %v, want no urls key", gotBody)
	}
}

func TestPurgeAllUnconfigured(t *testing.T) {
	t.Setenv("ZCRIBE_CF_ZONE", "")
	t.Setenv("ZCRIBE_CF_TOKEN", "")
	t.Setenv("ZCRIBE_PURGE_WEBHOOK", "")
	if _, err := PurgeAll(context.Background()); err == nil {
		t.Error("PurgeAll with nothing configured: want an error, got nil")
	}
}
