package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/indexer"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

func newServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := indexer.DefaultConfig()
	cfg.ConfirmationDepth = 3
	ix := indexer.New(cfg, nil, st, log)
	srv := New(st, ix, "http://content.test", log)
	srv.Scripts = true
	// Tests write rows straight into the store, which no indexer Progress
	// announces; checking the store on every request is what they need.
	srv.EVMMaxStale = 0
	return srv, st
}

// contentPath is the canonical, trailing-slash form. The slash is what makes
// an asset its own base URL, so a composite reaches import slot n with the
// relative reference "n".
func contentPath(id string) string { return "/content/" + id + "/" }

func seed(t *testing.T, st *store.Store) store.Asset {
	t.Helper()
	a := store.Asset{
		ID:     "aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900",
		Number: 0, Issuer: "z1issuer", Owner: "z1owner",
		Name: "probe", Supply: 7, Mime: "text/plain;charset=utf-8",
		Content: []byte("hello, momentum"), PartCount: 1, Height: 100, Timestamp: 1700000000,
	}
	if err := st.PutAsset(a); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBalance(a.ID, a.Owner, a.Supply); err != nil {
		t.Fatal(err)
	}
	return a
}

func do(t *testing.T, h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The content origin serves attacker-authored bytes, some of which execute
// script. Every one of these headers is load-bearing; a regression that drops
// one is a viewer compromise, not a cosmetic change.
func TestContentSecurityHeaders(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)
	h := s.ContentRoutes()

	rec := do(t, h, http.MethodGet, contentPath(a.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Body.String(); got != string(a.Content) {
		t.Fatalf("body = %q", got)
	}

	for header, want := range map[string]string{
		"Content-Type":           "text/plain;charset=utf-8",
		"X-Content-Type-Options": "nosniff",
		"Content-Disposition":    "inline",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.HasPrefix(csp, "sandbox") {
		t.Errorf("CSP must start with the sandbox directive, got %q", csp)
	}
	// Scripts are on, so the sandbox grants allow-scripts — and must still
	// withhold allow-same-origin, which is the only thing keeping one
	// inscription's script out of another inscription frame.
	if !strings.Contains(csp, "sandbox allow-scripts") {
		t.Errorf("recursion needs allow-scripts, got %q", csp)
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Fatalf("allow-same-origin must never be granted to inscribed content: %q", csp)
	}
	// Every fetch directive is pinned to the content origin, so an inscription
	// can compose itself from other inscriptions and from nothing else.
	for _, directive := range []string{
		"default-src http://content.test",
		"connect-src http://content.test",
		"script-src http://content.test",
		"form-action 'none'",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP missing %q: %s", directive, csp)
		}
	}
}

// With scripts off, nothing executes — the original behaviour, still reachable
// by an operator who does not want to run strangers' code.
func TestScriptsCanBeDisabled(t *testing.T) {
	s, st := newServer(t)
	s.Scripts = false
	a := seed(t, st)

	rec := do(t, s.ContentRoutes(), http.MethodGet, contentPath(a.ID), nil)
	csp := rec.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "allow-scripts") {
		t.Fatalf("scripts are disabled but the sandbox permits them: %q", csp)
	}
}

// The short form redirects to the canonical one. Without this, a page loaded
// as /content/{id} would resolve a relative import slot against /content/ and
// silently fetch a different asset.
func TestContentRedirectsToTrailingSlash(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)

	rec := do(t, s.ContentRoutes(), http.MethodGet, "/content/"+a.ID, nil)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != contentPath(a.ID) {
		t.Fatalf("Location = %q, want %q", loc, contentPath(a.ID))
	}
}

// A composite's import slots resolve to the imported asset's own canonical
// URL, by redirect rather than by copying bytes: one blob keeps one URL, so a
// cache stores it once and a moderation policy applies at the target.
func TestCompositeImportSlots(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)
	target := "bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900aa11"
	if err := st.PutRefs(a.ID, a.Height, []string{target}); err != nil {
		t.Fatal(err)
	}
	h := s.ContentRoutes()

	rec := do(t, h, http.MethodGet, "/content/"+a.ID+"/0", nil)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("slot 0 status %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != contentPath(target) {
		t.Fatalf("slot 0 -> %q, want %q", loc, contentPath(target))
	}
	// Asking for a slot the asset never declared is a 404, not a redirect
	// into nothing.
	if rec := do(t, h, http.MethodGet, "/content/"+a.ID+"/1", nil); rec.Code != http.StatusNotFound {
		t.Errorf("undeclared slot status %d, want 404", rec.Code)
	}
}

// The import graph is served in both directions, and a ref that names an id
// nobody has inscribed is reported as unresolved rather than as an error: refs
// are never validated at mint time, so a dangling edge is an ordinary state.
func TestRefsGraphBothDirections(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)
	dangling := "cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900aa11bb22"
	if err := st.PutRefs(a.ID, a.Height, []string{dangling}); err != nil {
		t.Fatal(err)
	}
	// Something importing the seeded asset, so the reverse edge is non-empty.
	if err := st.PutRefs(dangling, a.Height+1, []string{a.ID}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, s.Routes(nil), http.MethodGet, "/asset/"+a.ID+"/refs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got struct {
		Refs []struct {
			Slot     int    `json:"slot"`
			ID       string `json:"id"`
			URL      string `json:"url"`
			Resolved bool   `json:"resolved"`
		} `json:"refs"`
		ReferencedBy []string `json:"referencedBy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Refs) != 1 || got.Refs[0].ID != dangling {
		t.Fatalf("refs = %+v", got.Refs)
	}
	if got.Refs[0].Resolved {
		t.Error("a ref to an unindexed id must report resolved:false, not fail")
	}
	if got.Refs[0].URL != "http://content.test"+contentPath(dangling) {
		t.Errorf("ref url = %q", got.Refs[0].URL)
	}
	if len(got.ReferencedBy) != 1 || got.ReferencedBy[0] != dangling {
		t.Fatalf("referencedBy = %v", got.ReferencedBy)
	}
}

// The recursion endpoints live on the content origin, because a relative
// reference is the only kind an inscription can carry without hard-coding
// where this indexer happens to be deployed.
func TestRecursionEndpointsAreOnTheContentOrigin(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)
	h := s.ContentRoutes()

	for _, path := range []string{"/r/momentum", "/r/asset/" + a.ID, "/r/refs/" + a.ID} {
		if rec := do(t, h, http.MethodGet, path, nil); rec.Code != http.StatusOK {
			t.Errorf("%s status %d", path, rec.Code)
		}
	}
	// The tip moves, so it must never be cached the way immutable content is.
	rec := do(t, h, http.MethodGet, "/r/momentum", nil)
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("/r/momentum Cache-Control = %q, want no-store", cc)
	}
}

// The content origin must serve content and nothing else. If the metadata API
// routes leak onto it, a sandboxed inscription is same-origin with the API.
func TestContentOriginServesNothingElse(t *testing.T) {
	s, st := newServer(t)
	seed(t, st)
	h := s.ContentRoutes()

	for _, path := range []string{"/info", "/assets", "/", "/momentum/1"} {
		if rec := do(t, h, http.MethodGet, path, nil); rec.Code == http.StatusOK {
			t.Errorf("content origin answered %s with 200 — it must only serve /content/", path)
		}
	}
}

// Range support is what makes inscribed audio and video seekable rather than
// forcing a full download before playback starts.
func TestContentSupportsRangeRequests(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)
	h := s.ContentRoutes()

	rec := do(t, h, http.MethodGet, contentPath(a.ID), map[string]string{"Range": "bytes=7-11"})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "momen" {
		t.Fatalf("range body = %q, want %q", got, "momen")
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 7-11/15" {
		t.Fatalf("Content-Range = %q", cr)
	}
}

// Moderation is a serving-layer decision. Metadata withheld means the asset
// vanishes from the API; content withheld means the metadata still describes
// it but the bytes are refused. Neither touches the store or the state root.
func TestModerationAtTheServingLayer(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)
	api, content := s.Routes(nil), s.ContentRoutes()

	// Content withheld, metadata still served — and flagged.
	if err := st.SetModeration(a.ID, store.Moderation{MetadataPermitted: true, DataPermitted: false}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, api, http.MethodGet, "/asset/"+a.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata should still be served: %d", rec.Code)
	}
	var got assetJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.DataWithheld {
		t.Error("response should flag that the bytes are withheld")
	}
	if rec := do(t, content, http.MethodGet, contentPath(a.ID), nil); rec.Code != http.StatusGone {
		t.Errorf("content status %d, want 410", rec.Code)
	}

	// Metadata withheld: gone from both surfaces and from listings.
	if err := st.SetModeration(a.ID, store.Moderation{MetadataPermitted: false, DataPermitted: false}); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, api, http.MethodGet, "/asset/"+a.ID, nil); rec.Code != http.StatusGone {
		t.Errorf("metadata status %d, want 410", rec.Code)
	}
	rec = do(t, api, http.MethodGet, "/assets", nil)
	var list struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if list.Total != 0 {
		t.Errorf("hidden asset still in listings (total %d)", list.Total)
	}
}

// An asset resolves by id or by inscription number. The number path must not
// swallow a hex id that happens to be all digits.
func TestAssetLookupByIDAndNumber(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st)
	h := s.Routes(nil)

	for _, ref := range []string{a.ID, strings.ToUpper(a.ID), "0"} {
		rec := do(t, h, http.MethodGet, "/asset/"+ref, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("lookup by %q: status %d", ref, rec.Code)
		}
		var got assetJSON
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.ID != a.ID {
			t.Fatalf("lookup by %q returned %s", ref, got.ID)
		}
	}
	if rec := do(t, h, http.MethodGet, "/asset/999", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown number status %d, want 404", rec.Code)
	}
}

// Two confirmation levels, reported and never enforced.
//
// Soft means the indexer has parsed and applied the block: the inscription
// exists and renders now. Full means the configured depth of momentums sits
// above it. Both are served; the difference is what a client is entitled to
// claim about finality.
func TestSoftAndFullConfirmation(t *testing.T) {
	s, st := newServer(t)
	a := seed(t, st) // height 100, depth 3

	// syncHeight is 0 on a fresh indexer, so the asset is soft only.
	rec := do(t, s.Routes(nil), http.MethodGet, "/asset/"+a.ID, nil)
	var got assetJSON
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Confirmed || got.Status != "soft" {
		t.Errorf("status = %q confirmed = %v, want soft", got.Status, got.Confirmed)
	}
	if got.ContentURL != "http://content.test"+contentPath(a.ID) {
		t.Errorf("contentUrl = %q — clients must be told the content origin", got.ContentURL)
	}

	// Advance past the depth. The inscription momentum counts as the first
	// confirmation, so height+depth-1 is the first fully confirmed height.
	if err := st.PutMomentum(store.MomentumRow{
		Height: 102, Hash: "h", PreviousHash: "p", Timestamp: 1, StateRoot: "r",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ix.Run(cancelledContext()); err == nil {
		t.Fatal("Run should stop immediately on a cancelled context")
	}
	rec = do(t, s.Routes(nil), http.MethodGet, "/asset/"+a.ID, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Confirmed || got.Status != "full" {
		t.Errorf("status = %q confirmed = %v confirmations = %d, want full",
			got.Status, got.Confirmed, got.Confirmations)
	}
	if got.Confirmations != 3 {
		t.Errorf("confirmations = %d, want 3 (100, 101, 102)", got.Confirmations)
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestInfoReportsProtocolIdentity(t *testing.T) {
	s, _ := newServer(t)
	rec := do(t, s.Routes(nil), http.MethodGet, "/info", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var info infoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	// A client refuses an indexer whose protocol it does not recognise, so
	// these three fields are the contract.
	if info.Prefix != "glyph" || info.ProtocolVersion != 1 || info.Protocol != "zenoglyphs" {
		t.Fatalf("identity = %q v%d %q", info.Prefix, info.ProtocolVersion, info.Protocol)
	}
	if len(info.AllowedMimes) == 0 {
		t.Error("the mime allowlist must be published so clients can validate before inscribing")
	}
	if info.ContentOrigin == "" {
		t.Error("content origin must be published")
	}
}

func TestMomentumAndStateRoot(t *testing.T) {
	s, st := newServer(t)
	if err := st.PutMomentum(store.MomentumRow{
		Height: 42, Hash: "hh", PreviousHash: "pp", Timestamp: 5, StateRoot: "rr",
	}); err != nil {
		t.Fatal(err)
	}
	h := s.Routes(nil)

	rec := do(t, h, http.MethodGet, "/momentum/42", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var m struct {
		Height    uint64   `json:"height"`
		StateRoot string   `json:"stateRoot"`
		Txs       []string `json:"txs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Height != 42 || m.StateRoot != "rr" {
		t.Fatalf("momentum = %+v", m)
	}
	// A momentum with no inscriptions must return [] rather than null, or a
	// comparing client has to special-case the empty case.
	if m.Txs == nil {
		t.Error("txs should be an empty array, not null")
	}

	if rec := do(t, h, http.MethodGet, "/stateroot/42", nil); rec.Code != http.StatusOK {
		t.Errorf("stateroot status %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/stateroot/43", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unindexed height status %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/momentum/notanumber", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad height status %d, want 400", rec.Code)
	}
}
