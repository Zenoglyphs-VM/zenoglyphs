// The admin panel: everything `zcribe moderate` does, reachable from a
// browser at /admin instead of a shell on the machine running the indexer.
//
// It is deliberately not linked from the viewer and its existence is not
// published — see web/ui/admin — but the path is not the security boundary,
// a password is. /admin/api/ answers 503 until EnableAdmin is called with one
// (from ZCRIBE_ADMIN_PASSWORD, read in cmd/zcribe/serve.go — never a CLI
// flag, the same rule signing keys follow, because a flag value sits in shell
// history and the process table), so an operator who never sets a password
// gets no admin surface at all rather than one guarded by an empty string.
//
// Sessions are a cookie, which is new: nothing else in zcribe sets one (see
// deploy/README.md §1). It cannot reach the content origin — Path=/admin
// scopes it to this API origin's /admin tree and no Domain attribute is ever
// set, so it stays host-only. That matters more now than it used to, because
// the deployment this ships with puts both origins on subdomains of one
// registrable domain, where a cookie IS able to cross between them unless it
// is scoped to prevent it.
//
// The reverse direction — a hostile inscription setting a parent-domain
// cookie that arrives here — is handled in validSession, which checks every
// cookie of this name rather than the first.
//
// The panel never renders inscribed bytes on this origin, including for the
// operator's own review. That is not an oversight: the content an admin most
// needs to look at closely is, by construction, the content most likely to
// be adversarial, and this is the one origin a hostile inscription must never
// execute script on (spec R-8.2). Reviewing content before moderating it
// happens through the ordinary viewer, which already renders every class
// safely from the sandboxed content origin.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/moderation"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

const (
	adminCookieName = "zenoglyphs_admin"
	adminSessionTTL = 12 * time.Hour

	adminMaxAttempts = 5               // failures allowed inside the window below
	adminAttemptsIn  = 5 * time.Minute // window the failures are counted over
	adminLockoutFor  = 15 * time.Minute
	adminAttemptCap  = 10000 // bound on distinct IPs tracked at once

	// adminMinPasswordLen is enforced only on a change made through the panel
	// — a new, deliberate boundary with no back-compat cost. Startup with a
	// short ZCRIBE_ADMIN_PASSWORD still only warns (see cmd/zcribe/serve.go);
	// tightening that would break existing deployments on upgrade for no
	// benefit to anyone who already chose their own password out-of-band.
	adminMinPasswordLen = 12
)

// adminAuth is the panel's auth state: a password digest compared in
// constant time, and an HMAC secret that signs session cookies. The secret is
// persisted in the store's meta table so a restart does not sign every admin
// out; the password itself is never persisted anywhere.
type adminAuth struct {
	passwordDigest [32]byte
	secret         []byte

	mu       sync.Mutex
	attempts map[string]*loginAttempts

	// generation invalidates every outstanding session at once when it is
	// bumped (on logout). It is a counter rather than a "sessions issued
	// before this instant are void" timestamp deliberately: this process
	// observably cannot rely on wall-clock resolution finer than its own
	// request latency (two time.Now() calls a few instructions apart can
	// read back equal), which a timestamp cutoff needs and a counter never
	// does — a bump is unambiguously distinguishable from every generation
	// issued before it, tie or no tie.
	generation int64
}

type loginAttempts struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

// EnableAdmin turns on the admin panel with the given password. Call it once
// at startup; leaving it uncalled is how an operator opts out entirely.
//
// The password argument only ever seeds the FIRST password. If the panel's
// own "change password" has ever run, its digest was persisted (see
// handleAdminChangePassword) and takes over here — otherwise every restart
// would silently revert a change made through the UI back to whatever
// ZCRIBE_ADMIN_PASSWORD still says, which would make "change it from the
// panel" a feature that cannot survive an upgrade.
func (s *Server) EnableAdmin(password string) error {
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("admin password must not be empty")
	}
	secret, err := s.adminSecret()
	if err != nil {
		return err
	}
	generation, err := s.adminGeneration()
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(password))
	if persisted, err := s.adminPasswordDigest(); err != nil {
		return err
	} else if persisted != nil {
		digest = *persisted
	}
	s.admin = &adminAuth{
		passwordDigest: digest,
		secret:         secret,
		attempts:       make(map[string]*loginAttempts),
		generation:     generation,
	}
	return nil
}

// adminPasswordDigest loads a password digest changed via the panel, or nil
// if the password has never been changed that way (in which case
// ZCRIBE_ADMIN_PASSWORD, passed to EnableAdmin, is still authoritative).
func (s *Server) adminPasswordDigest() (*[32]byte, error) {
	const key = "admin_password_digest"
	v, err := s.st.Meta(key)
	if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(v)
	if err != nil || len(raw) != 32 {
		// Unset, or unreadable — either way there is nothing to trust here,
		// and that is not a reason to fail startup: fall back to the env var.
		return nil, nil
	}
	var digest [32]byte
	copy(digest[:], raw)
	return &digest, nil
}

// AdminEnabled reports whether EnableAdmin was called, so the startup log can
// tell the operator whether the panel is reachable at all.
func (s *Server) AdminEnabled() bool { return s.admin != nil }

// adminSecret loads the persisted HMAC secret, generating and storing one on
// first use. Reusing the store's existing meta table means this needed no
// schema change.
func (s *Server) adminSecret() ([]byte, error) {
	const key = "admin_session_secret"
	existing, err := s.st.Meta(key)
	if err != nil {
		return nil, err
	}
	if existing != "" {
		if secret, err := hex.DecodeString(existing); err == nil && len(secret) == 32 {
			return secret, nil
		}
		// Anything else (wrong length, not hex) is unreadable, not a reason
		// to fail startup — fall through and mint a fresh one.
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate admin session secret: %w", err)
	}
	if err := s.st.SetMeta(key, hex.EncodeToString(secret)); err != nil {
		return nil, fmt.Errorf("persist admin session secret: %w", err)
	}
	return secret, nil
}

// adminGeneration loads the session-revocation counter logout bumps, so that
// signing out stays signed out across a restart rather than only clearing
// the browser's copy of a token the server would still accept.
func (s *Server) adminGeneration() (int64, error) {
	v, err := s.st.Meta("admin_session_generation")
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, nil // unset or unreadable: start counting from 0
	}
	return n, nil
}

// checkPassword compares a candidate against the live digest in constant
// time. Reading passwordDigest under the same mutex that setPasswordDigest
// writes it under is what makes a password change (handleAdminChangePassword)
// safe against a login racing it — without the lock, a login mid-change could
// read a torn value that matches neither the old password nor the new one.
func (a *adminAuth) checkPassword(password string) bool {
	got := sha256.Sum256([]byte(password))
	a.mu.Lock()
	want := a.passwordDigest
	a.mu.Unlock()
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func (a *adminAuth) setPasswordDigest(digest [32]byte) {
	a.mu.Lock()
	a.passwordDigest = digest
	a.mu.Unlock()
}

// revokeAdminSessions invalidates every outstanding session cookie at once.
// There is exactly one admin identity behind the shared password, so "log
// out" and "log out everywhere" are the same operation — which is also what
// makes this the right response to a suspected leaked cookie.
func (s *Server) revokeAdminSessions() {
	s.admin.mu.Lock()
	s.admin.generation++
	gen := s.admin.generation
	s.admin.mu.Unlock()
	_ = s.st.SetMeta("admin_session_generation", strconv.FormatInt(gen, 10))
}

// adminRoutes is mounted at /admin/api/ by Routes, outside withCORS — see the
// package comment for why a cookie-authenticated surface must not answer
// Access-Control-Allow-Origin: *.
func (s *Server) adminRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/login", s.handleAdminLogin)
	mux.HandleFunc("POST /admin/api/logout", s.handleAdminLogout)
	mux.HandleFunc("GET /admin/api/session", s.handleAdminSession)

	protected := http.NewServeMux()
	protected.HandleFunc("GET /admin/api/status", s.handleAdminStatus)
	protected.HandleFunc("GET /admin/api/moderation", s.handleAdminModerationList)
	protected.HandleFunc("GET /admin/api/moderation/export", s.handleAdminModerationExport)
	protected.HandleFunc("POST /admin/api/moderation/import", s.handleAdminModerationImport)
	protected.HandleFunc("GET /admin/api/moderation/{id}", s.handleAdminModerationGet)
	protected.HandleFunc("POST /admin/api/moderation/{id}", s.handleAdminModerationSet)
	protected.HandleFunc("POST /admin/api/purge", s.handleAdminPurge)
	protected.HandleFunc("POST /admin/api/purge-all", s.handleAdminPurgeAll)
	protected.HandleFunc("POST /admin/api/password", s.handleAdminChangePassword)
	mux.Handle("/admin/api/", s.requireAdmin(protected))

	return noStore(mux)
}

func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every admin response, including error bodies, may describe what is
		// being moderated and why — a shared or corporate proxy caching that
		// is not a risk this panel needs to take on.
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// requireAdmin gates every route but login/logout/session on a valid cookie,
// and slides its expiry forward on each authenticated request so an admin
// working through a long moderation queue is not signed out mid-task.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.admin == nil {
			writeErr(w, http.StatusServiceUnavailable, "admin panel not configured")
			return
		}
		if !s.admin.validSession(r) {
			writeErr(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		s.admin.issueSession(w, r)
		next.ServeHTTP(w, r)
	})
}

// --- session cookie: a 16-byte (generation, issuedAt) payload, HMAC-SHA256-signed ---

func (a *adminAuth) issueSession(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	gen := a.generation
	a.mu.Unlock()

	var payload [16]byte
	binary.BigEndian.PutUint64(payload[0:8], uint64(gen))
	binary.BigEndian.PutUint64(payload[8:16], uint64(time.Now().UnixNano()))
	mac := hmac.New(sha256.New, a.secret)
	mac.Write(payload[:])
	value := hex.EncodeToString(payload[:]) + "." + hex.EncodeToString(mac.Sum(nil))

	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    value,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(adminSessionTTL.Seconds()),
	})
}

// validSession accepts the request if ANY cookie of this name validates, not
// just the first.
//
// The distinction matters on a deployment that puts the API and the content
// origin on two subdomains of one registrable domain (deploy/README.md §1).
// Cookies are scoped per *site*, not per origin, so script in an inscription
// served from the content subdomain can set a cookie named adminCookieName
// with `Domain=` the shared parent — which the browser then sends here
// alongside the operator's real one. It cannot forge a *valid* session: the
// value is HMAC-signed with a secret it never sees. What it could do, if this
// read only the first match, is shadow the real cookie and lock the operator
// out of their own panel, since a Cookie header carries no indication of
// which host set each entry.
//
// Checking all of them costs one HMAC per junk cookie and removes the
// shadowing entirely. It is not a weakening: an attacker who cannot produce a
// signature gains nothing from being one of several candidates.
//
// The `__Host-` cookie prefix is the other fix — browsers reject a `__Host-`
// cookie that carries a `Domain` — but it also mandates `Path=/`, which would
// give up the `Path=/admin` scoping in issueSession that keeps this cookie off
// every ordinary API and viewer request. This keeps both.
func (a *adminAuth) validSession(r *http.Request) bool {
	for _, c := range r.CookiesNamed(adminCookieName) {
		if a.validSessionValue(c.Value) {
			return true
		}
	}
	return false
}

func (a *adminAuth) validSessionValue(value string) bool {
	sep := strings.IndexByte(value, '.')
	if sep < 0 {
		return false
	}
	payload, err := hex.DecodeString(value[:sep])
	if err != nil || len(payload) != 16 {
		return false
	}
	sig, err := hex.DecodeString(value[sep+1:])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, a.secret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}

	gen := int64(binary.BigEndian.Uint64(payload[0:8]))
	issuedAt := int64(binary.BigEndian.Uint64(payload[8:16]))

	a.mu.Lock()
	current := a.generation
	a.mu.Unlock()

	// Equality, not a range check: a session belongs to exactly the
	// generation live when it was issued, and a logout (which bumps the
	// generation) must invalidate it regardless of how the two events order
	// against wall-clock time.
	if gen != current {
		return false
	}
	return time.Now().UnixNano() < issuedAt+adminSessionTTL.Nanoseconds()
}

func clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// isHTTPS decides the cookie's Secure flag. TLS terminates at the reverse
// proxy in every deployment this ships (see deploy/), so the connection this
// process sees is always plain HTTP; X-Forwarded-Proto is the only signal
// that the browser's connection was not. Trusting it only ever loosens
// Secure to match reality — it grants nothing an attacker could not already
// reach — so there is no need to configure which proxies to trust.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// --- login rate limiting, keyed best-effort by client IP ---
//
// This is not the authentication boundary; the password is. A spoofed
// X-Forwarded-For only ever degrades this to a shared bucket, never grants
// access to anyone who does not already have the password.

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i >= 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (a *adminAuth) allowAttempt(ip string) (bool, time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.attempts) > adminAttemptCap {
		a.attempts = make(map[string]*loginAttempts)
	}
	now := time.Now()
	la, ok := a.attempts[ip]
	if !ok {
		return true, 0
	}
	if now.Before(la.lockedUntil) {
		return false, la.lockedUntil.Sub(now)
	}
	if now.Sub(la.windowStart) > adminAttemptsIn {
		delete(a.attempts, ip)
	}
	return true, 0
}

func (a *adminAuth) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	la := a.attempts[ip]
	if la == nil || now.Sub(la.windowStart) > adminAttemptsIn {
		la = &loginAttempts{windowStart: now}
		a.attempts[ip] = la
	}
	la.count++
	if la.count >= adminMaxAttempts {
		la.lockedUntil = now.Add(adminLockoutFor)
	}
}

func (a *adminAuth) recordSuccess(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.attempts, ip)
}

// --- handlers: session ---

type adminLoginRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if s.admin == nil {
		writeErr(w, http.StatusServiceUnavailable, "admin panel not configured")
		return
	}
	ip := clientIP(r)
	if ok, wait := s.admin.allowAttempt(ip); !ok {
		writeErr(w, http.StatusTooManyRequests,
			fmt.Sprintf("too many attempts; try again in %s", wait.Round(time.Second)))
		return
	}
	var req adminLoginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !s.admin.checkPassword(req.Password) {
		s.admin.recordFailure(ip)
		writeErr(w, http.StatusUnauthorized, "invalid password")
		return
	}
	s.admin.recordSuccess(ip)
	s.admin.issueSession(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	// Revokes every outstanding session, not just this browser's copy of the
	// cookie — see revokeAdminSessions.
	s.revokeAdminSessions()
	clearSession(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":       s.admin != nil,
		"authenticated": s.admin != nil && s.admin.validSession(r),
	})
}

type adminChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// handleAdminChangePassword is the only way this password is ever changed —
// there is no separate "reset" path, because a reset that does not require
// the current password would make the current password pointless. It is
// gated by requireAdmin like every other route on this mux (a valid session
// cookie is required just to reach it) and additionally re-checks the
// current password itself: a session cookie proves "was logged in", not
// "still is the operator", and the two diverge exactly in the case this
// endpoint exists to protect against — a stolen or left-open session.
//
// The new digest is persisted (see adminPasswordDigest) so the change
// survives a restart, and every OTHER outstanding session is revoked — the
// same "log out everywhere" revokeAdminSessions already does for an explicit
// logout, which is the right response here too: a leaked cookie plus a
// leaked password should not both go on working. This request's own session
// is reissued immediately afterward so the browser that just changed the
// password is not the one that gets logged out by doing so.
func (s *Server) handleAdminChangePassword(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ok, wait := s.admin.allowAttempt(ip); !ok {
		writeErr(w, http.StatusTooManyRequests,
			fmt.Sprintf("too many attempts; try again in %s", wait.Round(time.Second)))
		return
	}

	var req adminChangePasswordRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !s.admin.checkPassword(req.CurrentPassword) {
		s.admin.recordFailure(ip)
		writeErr(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if len(req.NewPassword) < adminMinPasswordLen {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("new password must be at least %d characters", adminMinPasswordLen))
		return
	}
	if req.NewPassword == req.CurrentPassword {
		writeErr(w, http.StatusBadRequest, "new password must differ from the current one")
		return
	}
	s.admin.recordSuccess(ip)

	digest := sha256.Sum256([]byte(req.NewPassword))
	if err := s.st.SetMeta("admin_password_digest", hex.EncodeToString(digest[:])); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.admin.setPasswordDigest(digest)

	s.revokeAdminSessions()
	s.admin.issueSession(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- handlers: status ---

type adminSnapshotJSON struct {
	Height    uint64 `json:"height"`
	Depth     string `json:"depth"`
	Events    int64  `json:"events"`
	CreatedAt int64  `json:"createdAt"`
}

func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	tip, err := s.st.TipHeight()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	snaps, err := s.st.ListSnapshots()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	snapOut := make([]adminSnapshotJSON, 0, len(snaps))
	for _, sn := range snaps {
		depth := "-"
		if tip >= sn.Height {
			depth = strconv.FormatUint(tip-sn.Height, 10)
		}
		snapOut = append(snapOut, adminSnapshotJSON{
			Height: sn.Height, Depth: depth, Events: sn.Events, CreatedAt: sn.CreatedAt,
		})
	}
	modRows, err := s.st.ListModeration()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"info":            s.buildInfo(),
		"tipHeight":       tip,
		"moderatedCount":  len(modRows),
		"snapshots":       snapOut,
		"purgeConfigured": moderation.Configured(),
	})
}

// --- handlers: moderation ---

// moderationStatus collapses the store's two independent permission bits
// into the three-way choice the panel and CLI both actually offer
// (spec/ASSUMPTIONS.md: hiding metadata implies hiding content, so the fourth
// combination — metadata withheld, content permitted — never occurs here).
func moderationStatus(m store.Moderation) string {
	switch {
	case !m.MetadataPermitted:
		return "hidden"
	case !m.DataPermitted:
		return "redacted"
	default:
		return "visible"
	}
}

func policyFor(status, reason string) (store.Moderation, error) {
	switch status {
	case "visible":
		return store.Moderation{MetadataPermitted: true, DataPermitted: true, Reason: reason}, nil
	case "redacted":
		return store.Moderation{MetadataPermitted: true, DataPermitted: false, Reason: reason}, nil
	case "hidden":
		return store.Moderation{MetadataPermitted: false, DataPermitted: false, Reason: reason}, nil
	default:
		return store.Moderation{}, fmt.Errorf("status must be one of visible, redacted, hidden")
	}
}

func looksLikeAssetID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

type adminModerationRowJSON struct {
	AssetID string `json:"assetId"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Found   bool   `json:"found"`
	Name    string `json:"name,omitempty"`
}

func (s *Server) handleAdminModerationList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.ListModeration()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]adminModerationRowJSON, 0, len(rows))
	for _, row := range rows {
		item := adminModerationRowJSON{
			AssetID: row.AssetID, Status: moderationStatus(row.Moderation), Reason: row.Reason,
		}
		if a, err := s.st.Asset(row.AssetID); err == nil && a != nil {
			item.Found, item.Name = true, a.Name
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "moderation": out})
}

// handleAdminModerationGet resolves an id or inscription number and reports
// its current policy, for the panel's "look up an asset" search. A pattern
// that resolves to nothing is still answered, not 404ed outright, as long as
// it is a 64-character hex id — that is exactly what a pre-emptive block
// needs to work against an id this indexer has not reached yet.
func (s *Server) handleAdminModerationGet(w http.ResponseWriter, r *http.Request) {
	raw := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	a, err := s.lookupAsset(raw)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch {
	case a != nil:
		raw = a.ID
	case !looksLikeAssetID(raw):
		writeErr(w, http.StatusNotFound,
			"not a known inscription number, and not a 64-character hex asset id to pre-block")
		return
	}

	mod, err := s.st.Moderation(raw)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]any{
		"assetId": raw, "found": a != nil,
		"status": moderationStatus(mod), "reason": mod.Reason,
	}
	if a != nil {
		resp["asset"] = s.assetToJSON(*a)
		resp["galleryUrl"] = "/#/asset/" + a.ID
	}
	writeJSON(w, http.StatusOK, resp)
}

type adminSetModerationRequest struct {
	Status string `json:"status"` // visible | redacted | hidden
	Reason string `json:"reason"`
	Unseen bool   `json:"unseen"`
	Purge  bool   `json:"purge"`
}

func (s *Server) handleAdminModerationSet(w http.ResponseWriter, r *http.Request) {
	raw := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	var req adminSetModerationRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	a, err := s.lookupAsset(raw)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch {
	case a != nil:
		raw = a.ID
	case !looksLikeAssetID(raw):
		writeErr(w, http.StatusNotFound,
			"not a known inscription number, and not a 64-character hex asset id to pre-block")
		return
	case !req.Unseen:
		// Same refusal as `zcribe moderate` without -unseen: an id this
		// indexer has never heard of is usually a typo, not a pre-block.
		writeErr(w, http.StatusConflict,
			"no asset with this id has been indexed yet; pass unseen to block it pre-emptively")
		return
	}

	policy, err := policyFor(req.Status, req.Reason)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.SetModeration(raw, policy); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := map[string]any{"ok": true, "assetId": raw, "status": moderationStatus(policy)}
	if !policy.DataPermitted {
		resp["purge"] = s.maybePurge(r.Context(), []string{raw}, req.Purge)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAdminModerationExport(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.ListModeration()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	entries := make([]moderation.Entry, 0, len(rows))
	for _, row := range rows {
		if row.MetadataPermitted && row.DataPermitted {
			continue
		}
		entries = append(entries, moderation.Entry{ID: row.AssetID, Reason: row.Reason})
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="zcribe-blocklist.txt"`)
	moderation.WriteBlocklist(w, entries)
}

// handleAdminModerationImport applies a whole pasted or uploaded blocklist —
// the body is the same text format -export writes, read client-side from a
// file input so this endpoint never has to parse multipart.
func (s *Server) handleAdminModerationImport(w http.ResponseWriter, r *http.Request) {
	entries, err := moderation.ParseBlocklist(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(entries) == 0 {
		writeErr(w, http.StatusBadRequest, "no ids found in blocklist")
		return
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		reason := e.Reason
		if reason == "" {
			reason = "imported blocklist"
		}
		// A shared blocklist is always a full block, matching -import: it
		// exists for material an operator must not serve at all.
		if err := s.st.SetModeration(e.ID, store.Moderation{Reason: reason}); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		ids = append(ids, e.ID)
	}
	purge := r.URL.Query().Get("purge") == "1"
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "blocked": len(ids), "ids": ids,
		"purge": s.maybePurge(r.Context(), ids, purge),
	})
}

type adminPurgeRequest struct {
	IDs []string `json:"ids"`
}

// handleAdminPurge re-purges the CDN for ids whose policy was already set —
// useful when a purge failed earlier or the cache needs clearing again
// without changing anything.
func (s *Server) handleAdminPurge(w http.ResponseWriter, r *http.Request) {
	var req adminPurgeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "ids must not be empty")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purge": s.maybePurge(r.Context(), req.IDs, true)})
}

// handleAdminPurgeAll flushes the entire CDN cache rather than specific ids —
// see moderation.PurgeAll for when that is actually the right tool. It takes
// no body: unlike a targeted purge, there is no list of ids to get wrong.
func (s *Server) handleAdminPurgeAll(w http.ResponseWriter, r *http.Request) {
	backend, err := moderation.PurgeAll(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"purge": purgeResultJSON{Attempted: true, OK: false, Error: err.Error()},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"purge": purgeResultJSON{Attempted: true, OK: true, Backend: backend},
	})
}

type purgeResultJSON struct {
	Attempted bool     `json:"attempted"`
	OK        bool     `json:"ok"`
	Backend   string   `json:"backend,omitempty"`
	Error     string   `json:"error,omitempty"`
	URLs      []string `json:"urls"`
}

func (s *Server) maybePurge(ctx context.Context, ids []string, purge bool) purgeResultJSON {
	result := purgeResultJSON{URLs: moderation.URLsFor(s.ContentOrigin, ids)}
	if !purge {
		return result
	}
	result.Attempted = true
	backend, err := moderation.Purge(ctx, result.URLs)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.OK, result.Backend = true, backend
	return result
}
