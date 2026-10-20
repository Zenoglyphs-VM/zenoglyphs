package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// adminServer is newServer with the admin panel turned on, which is what
// every test in this file needs.
func adminServer(t *testing.T) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	s, _ := newServer(t)
	if err := s.EnableAdmin("correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	h := s.Routes(nil)
	cookie := login(t, h, "correct horse battery staple")
	return s, h, cookie
}

func login(t *testing.T, h http.Handler, password string) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": password})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == adminCookieName {
			return c
		}
	}
	t.Fatal("login did not set a session cookie")
	return nil
}

func authed(t *testing.T, h http.Handler, cookie *http.Cookie, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestAdminDisabledByDefault(t *testing.T) {
	s, _ := newServer(t)
	h := s.Routes(nil)
	if s.AdminEnabled() {
		t.Fatal("AdminEnabled() = true before EnableAdmin was called")
	}
	rec := authed(t, h, nil, http.MethodGet, "/admin/api/status", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestAdminLoginRequiresCorrectPassword(t *testing.T) {
	s, _ := newServer(t)
	if err := s.EnableAdmin("the-real-password"); err != nil {
		t.Fatal(err)
	}
	h := s.Routes(nil)

	body, _ := json.Marshal(map[string]string{"password": "wrong"})
	rec := authed(t, h, nil, http.MethodPost, "/admin/api/login", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == adminCookieName {
			t.Fatal("a cookie was set for a failed login")
		}
	}
}

func TestAdminLoginLockout(t *testing.T) {
	s, _ := newServer(t)
	if err := s.EnableAdmin("the-real-password"); err != nil {
		t.Fatal(err)
	}
	h := s.Routes(nil)
	wrong, _ := json.Marshal(map[string]string{"password": "wrong"})

	var last *httptest.ResponseRecorder
	for i := 0; i < adminMaxAttempts; i++ {
		last = authed(t, h, nil, http.MethodPost, "/admin/api/login", wrong)
	}
	if last.Code != http.StatusUnauthorized {
		t.Fatalf("attempt %d: status = %d, want 401", adminMaxAttempts, last.Code)
	}

	// One more attempt, even with the correct password, must now be locked
	// out rather than let through.
	right, _ := json.Marshal(map[string]string{"password": "the-real-password"})
	rec := authed(t, h, nil, http.MethodPost, "/admin/api/login", right)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked-out attempt: status = %d, want 429, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAdminSessionRequiredForProtectedRoutes(t *testing.T) {
	s, _ := newServer(t)
	if err := s.EnableAdmin("pw"); err != nil {
		t.Fatal(err)
	}
	h := s.Routes(nil)

	rec := authed(t, h, nil, http.MethodGet, "/admin/api/status", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: status = %d, want 401", rec.Code)
	}

	rec = authed(t, h, &http.Cookie{Name: adminCookieName, Value: "garbage"},
		http.MethodGet, "/admin/api/status", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("garbage cookie: status = %d, want 401", rec.Code)
	}
}

func TestAdminSessionCookieIsScopedAndHardened(t *testing.T) {
	s, _ := newServer(t)
	if err := s.EnableAdmin("pw"); err != nil {
		t.Fatal(err)
	}
	h := s.Routes(nil)
	cookie := login(t, h, "pw")

	if cookie.Path != "/admin" {
		t.Errorf("cookie Path = %q, want /admin", cookie.Path)
	}
	if cookie.Domain != "" {
		t.Errorf("cookie Domain = %q, want unset (host-only)", cookie.Domain)
	}
	if !cookie.HttpOnly {
		t.Error("cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie SameSite = %v, want Strict", cookie.SameSite)
	}
}

func TestAdminLoginLogoutRoundTrip(t *testing.T) {
	s, h, cookie := adminServer(t)
	_ = s

	rec := authed(t, h, cookie, http.MethodGet, "/admin/api/session", nil)
	var session struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if !session.Authenticated {
		t.Fatal("session reports not authenticated right after login")
	}

	authed(t, h, cookie, http.MethodPost, "/admin/api/logout", nil)
	rec = authed(t, h, cookie, http.MethodGet, "/admin/api/status", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status after logout = %d, want 401", rec.Code)
	}
}

func TestAdminRoutesExcludedFromWildcardCORS(t *testing.T) {
	_, h, cookie := adminServer(t)
	rec := authed(t, h, cookie, http.MethodGet, "/admin/api/status", nil)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q on an admin route, want unset", got)
	}
	// The public API keeps it, so the exclusion above is specific to admin
	// routes rather than a regression that dropped CORS everywhere.
	rec = authed(t, h, nil, http.MethodGet, "/info", nil)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("public /info Access-Control-Allow-Origin = %q, want *", got)
	}
}

func TestAdminModerationSetRedactKeepsAssetSearchableButHidesContent(t *testing.T) {
	s, h, cookie := adminServer(t)
	a := seed(t, s.st)

	body, _ := json.Marshal(map[string]any{"status": "redacted", "reason": "reported"})
	rec := authed(t, h, cookie, http.MethodPost, "/admin/api/moderation/"+a.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("set redacted: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// The public listing must still surface it — "redacted" means searchable,
	// content withheld, not hidden outright. This is the behaviour the admin
	// action is required to produce, and it is asserted again at the store
	// layer in store_test.go; the public /assets and /asset/{id} responses
	// are the promise this test is actually checking.
	listRec := authed(t, h, nil, http.MethodGet, "/assets", nil)
	var list struct {
		Assets []struct {
			ID           string `json:"id"`
			DataWithheld bool   `json:"dataWithheld"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list.Assets {
		if item.ID == a.ID {
			found = true
			if !item.DataWithheld {
				t.Error("redacted asset listed without dataWithheld")
			}
		}
	}
	if !found {
		t.Error("redacted asset no longer appears in /assets — should still be searchable")
	}

	assetRec := authed(t, h, nil, http.MethodGet, "/asset/"+a.ID, nil)
	if assetRec.Code != http.StatusOK {
		t.Fatalf("GET /asset/%s after redact = %d, want 200 (metadata stays visible)", a.ID, assetRec.Code)
	}

	// Content bytes are served by the separate content origin (spec R-8.2),
	// not the mux under test above.
	contentRec := authed(t, s.ContentRoutes(), nil, http.MethodGet, "/content/"+a.ID+"/", nil)
	if contentRec.Code != http.StatusGone {
		t.Fatalf("content status after redact = %d, want 410", contentRec.Code)
	}
}

func TestAdminModerationSetHiddenRemovesFromListing(t *testing.T) {
	s, h, cookie := adminServer(t)
	a := seed(t, s.st)

	body, _ := json.Marshal(map[string]any{"status": "hidden", "reason": "illegal material"})
	rec := authed(t, h, cookie, http.MethodPost, "/admin/api/moderation/"+a.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("set hidden: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	listRec := authed(t, h, nil, http.MethodGet, "/assets", nil)
	if strings.Contains(listRec.Body.String(), a.ID) {
		t.Error("hidden asset still appears in /assets")
	}
	assetRec := authed(t, h, nil, http.MethodGet, "/asset/"+a.ID, nil)
	if assetRec.Code != http.StatusGone {
		t.Fatalf("GET /asset/%s after hide = %d, want 410", a.ID, assetRec.Code)
	}
}

func TestAdminModerationUnseenPreBlock(t *testing.T) {
	_, h, cookie := adminServer(t)
	id := strings.Repeat("ab", 32) // 64 hex chars, never indexed

	body, _ := json.Marshal(map[string]any{"status": "hidden", "reason": "pre-block"})
	rec := authed(t, h, cookie, http.MethodPost, "/admin/api/moderation/"+id, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("without unseen: status = %d, want 409", rec.Code)
	}

	body, _ = json.Marshal(map[string]any{"status": "hidden", "reason": "pre-block", "unseen": true})
	rec = authed(t, h, cookie, http.MethodPost, "/admin/api/moderation/"+id, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("with unseen: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAdminModerationSetRejectsGarbageID(t *testing.T) {
	_, h, cookie := adminServer(t)
	body, _ := json.Marshal(map[string]any{"status": "hidden", "unseen": true})
	rec := authed(t, h, cookie, http.MethodPost, "/admin/api/moderation/not-an-id", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestAdminModerationImportAndExportRoundTrip(t *testing.T) {
	_, h, cookie := adminServer(t)
	id1, id2 := strings.Repeat("aa", 32), strings.Repeat("bb", 32)
	blocklist := id1 + " # first\n" + id2 + " # second\n"

	rec := authed(t, h, cookie, http.MethodPost, "/admin/api/moderation/import", []byte(blocklist))
	if rec.Code != http.StatusOK {
		t.Fatalf("import: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var imported struct {
		Blocked int `json:"blocked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Blocked != 2 {
		t.Fatalf("blocked = %d, want 2", imported.Blocked)
	}

	rec = authed(t, h, cookie, http.MethodGet, "/admin/api/moderation/export", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), id1) || !strings.Contains(rec.Body.String(), id2) {
		t.Errorf("export body missing an imported id: %s", rec.Body.String())
	}
}

func TestAdminModerationListOnlyShowsRestrictedRows(t *testing.T) {
	s, h, cookie := adminServer(t)
	a := seed(t, s.st)

	rec := authed(t, h, cookie, http.MethodGet, "/admin/api/moderation", nil)
	var before struct {
		Count int `json:"count"`
	}
	json.Unmarshal(rec.Body.Bytes(), &before) //nolint:errcheck

	body, _ := json.Marshal(map[string]any{"status": "redacted"})
	authed(t, h, cookie, http.MethodPost, "/admin/api/moderation/"+a.ID, body)

	rec = authed(t, h, cookie, http.MethodGet, "/admin/api/moderation", nil)
	var after struct {
		Count      int                      `json:"count"`
		Moderation []adminModerationRowJSON `json:"moderation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.Count != before.Count+1 {
		t.Fatalf("count = %d, want %d", after.Count, before.Count+1)
	}

	// Setting it back to visible must drop it from the list again — a
	// fully-permitted row is indistinguishable from no row at all.
	body, _ = json.Marshal(map[string]any{"status": "visible"})
	authed(t, h, cookie, http.MethodPost, "/admin/api/moderation/"+a.ID, body)
	rec = authed(t, h, cookie, http.MethodGet, "/admin/api/moderation", nil)
	json.Unmarshal(rec.Body.Bytes(), &after) //nolint:errcheck
	if after.Count != before.Count {
		t.Fatalf("count after allow = %d, want %d", after.Count, before.Count)
	}
}

func TestAdminModerationGetByNumber(t *testing.T) {
	s, h, cookie := adminServer(t)
	a := seed(t, s.st)

	rec := authed(t, h, cookie, http.MethodGet, "/admin/api/moderation/0", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("lookup by number: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		AssetID string `json:"assetId"`
		Found   bool   `json:"found"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.AssetID != a.ID {
		t.Errorf("got %+v, want found asset %s", got, a.ID)
	}
}

// --- change password ---

func changePassword(t *testing.T, h http.Handler, cookie *http.Cookie, current, next string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"currentPassword": current, "newPassword": next})
	return authed(t, h, cookie, http.MethodPost, "/admin/api/password", body)
}

func TestAdminChangePasswordRequiresCurrentPassword(t *testing.T) {
	_, h, cookie := adminServer(t)
	rec := changePassword(t, h, cookie, "wrong current password", "a perfectly fine new password")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}

	// A rejected attempt must not touch the live password: the original
	// login and session both still have to work afterward.
	rec = authed(t, h, cookie, http.MethodGet, "/admin/api/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("session after failed change: status = %d, want 200", rec.Code)
	}
}

func TestAdminChangePasswordRejectsShortPassword(t *testing.T) {
	_, h, cookie := adminServer(t)
	rec := changePassword(t, h, cookie, "correct horse battery staple", "short")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAdminChangePasswordRejectsSameAsCurrent(t *testing.T) {
	_, h, cookie := adminServer(t)
	rec := changePassword(t, h, cookie, "correct horse battery staple", "correct horse battery staple")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestAdminChangePasswordPersistsAcrossRestart is the point of persisting the
// digest at all: EnableAdmin is called a second time with the OLD password,
// simulating `zcribe serve` starting back up with an unchanged
// ZCRIBE_ADMIN_PASSWORD, and the NEW password must still be the one that
// works — see adminPasswordDigest.
func TestAdminChangePasswordPersistsAcrossRestart(t *testing.T) {
	s, h, cookie := adminServer(t)

	rec := changePassword(t, h, cookie, "correct horse battery staple", "a brand new admin password")
	if rec.Code != http.StatusOK {
		t.Fatalf("change: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Same env-var password as before — a real restart would pass exactly
	// this, unchanged, from ZCRIBE_ADMIN_PASSWORD.
	if err := s.EnableAdmin("correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	h2 := s.Routes(nil)

	if rec := authed(t, h2, nil, http.MethodPost, "/admin/api/login",
		mustJSON(t, map[string]string{"password": "correct horse battery staple"})); rec.Code != http.StatusUnauthorized {
		t.Errorf("old password after 'restart': status = %d, want 401", rec.Code)
	}
	if rec := authed(t, h2, nil, http.MethodPost, "/admin/api/login",
		mustJSON(t, map[string]string{"password": "a brand new admin password"})); rec.Code != http.StatusOK {
		t.Errorf("new password after 'restart': status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
}

// TestAdminChangePasswordRevokesOtherSessions is "log out everywhere" applied
// to a password change: a second, independently-logged-in session must stop
// working, while the session that performed the change is reissued a fresh
// cookie rather than being logged out by its own request.
func TestAdminChangePasswordRevokesOtherSessions(t *testing.T) {
	s, h, cookieA := adminServer(t)
	_ = s
	cookieB := login(t, h, "correct horse battery staple")

	rec := changePassword(t, h, cookieA, "correct horse battery staple", "a brand new admin password")
	if rec.Code != http.StatusOK {
		t.Fatalf("change: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var freshCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == adminCookieName {
			freshCookie = c
		}
	}
	if freshCookie == nil {
		t.Fatal("change-password response did not reissue a session cookie")
	}

	if rec := authed(t, h, cookieB, http.MethodGet, "/admin/api/status", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("session B after change: status = %d, want 401", rec.Code)
	}
	if rec := authed(t, h, cookieA, http.MethodGet, "/admin/api/status", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("session A's PRE-change cookie after change: status = %d, want 401", rec.Code)
	}
	if rec := authed(t, h, freshCookie, http.MethodGet, "/admin/api/status", nil); rec.Code != http.StatusOK {
		t.Errorf("the reissued cookie: status = %d, want 200", rec.Code)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// --- purge-all ---

func TestAdminPurgeAllUnconfigured(t *testing.T) {
	t.Setenv("ZCRIBE_CF_ZONE", "")
	t.Setenv("ZCRIBE_CF_TOKEN", "")
	t.Setenv("ZCRIBE_PURGE_WEBHOOK", "")
	_, h, cookie := adminServer(t)

	// Unconfigured is reported IN the response body, not as an HTTP error —
	// same convention handleAdminModerationSet's purge field already follows,
	// so the panel can show "purge failed: ..." instead of a blanket failure.
	rec := authed(t, h, cookie, http.MethodPost, "/admin/api/purge-all", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Purge purgeResultJSON `json:"purge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Purge.Attempted || body.Purge.OK || body.Purge.Error == "" {
		t.Errorf("purge = %+v, want attempted, not ok, with an error", body.Purge)
	}
}

func TestAdminPurgeAllRequiresAuth(t *testing.T) {
	_, h, _ := adminServer(t)
	rec := authed(t, h, nil, http.MethodPost, "/admin/api/purge-all", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// A hostile inscription on a sibling subdomain can set a cookie of this name
// scoped to the shared parent domain, which the browser then sends here
// alongside the operator's real one. It cannot forge a valid session, but if
// the server read only the first match it could shadow the real cookie and
// lock the operator out. See validSession and deploy/README.md §1.
func TestAdminSessionSurvivesShadowingCookie(t *testing.T) {
	_, h, cookie := adminServer(t)

	injected := &http.Cookie{Name: adminCookieName, Value: "attacker-set-junk"}

	for _, order := range []struct {
		name    string
		cookies []*http.Cookie
	}{
		{"injected first", []*http.Cookie{injected, cookie}},
		{"injected last", []*http.Cookie{cookie, injected}},
	} {
		t.Run(order.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/admin/api/session", nil)
			for _, c := range order.cookies {
				r.AddCookie(c)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var got struct {
				Authenticated bool `json:"authenticated"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !got.Authenticated {
				t.Fatal("a junk cookie of the same name shadowed the real session")
			}
		})
	}
}

// The other half: junk alone must still be rejected. Accepting any valid
// cookie must not become accepting any cookie.
func TestAdminSessionRejectsOnlyShadowCookies(t *testing.T) {
	_, h, _ := adminServer(t)

	r := httptest.NewRequest(http.MethodGet, "/admin/api/moderation", nil)
	r.AddCookie(&http.Cookie{Name: adminCookieName, Value: "attacker-set-junk"})
	r.AddCookie(&http.Cookie{Name: adminCookieName, Value: "more.junk"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
