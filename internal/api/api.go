// Package api serves indexed state over HTTP.
//
// Two origins, not one. The metadata API and the content endpoint are
// separate servers on separate ports because the MIME allowlist includes
// text/html and image/svg+xml, both of which execute script. Serving
// attacker-authored HTML from the same origin as the API would give that
// script the API's origin — and with it, anything the viewer's browser will
// do on that origin's behalf.
package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/indexer"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// Version is this indexer build's own version, distinct from the protocol
// version. A client checks the protocol version to decide whether to trust an
// indexer at all, and this one only for bug reports.
const Version = "0.1.0"

type Server struct {
	st  *store.Store
	ix  *indexer.Indexer
	log *slog.Logger

	// evm caches the derived EVM state. It is a view, never a source: see
	// internal/api/evmcache.go.
	evm evmCache

	// EVMMaxStale bounds how long the EVM cache may skip checking the store
	// while the in-process indexer has committed nothing. It is what catches a
	// rewrite made by another process (`zcribe reindex`); zero checks on every
	// request. EVMCheckpointEvery is how many EVM inscriptions pass between
	// persisted checkpoints of derived state. New sets both.
	EVMMaxStale        time.Duration
	EVMCheckpointEvery int

	// ContentOrigin is the base URL clients should use for content bytes.
	// Published in /info so the front end never has to guess it.
	ContentOrigin string

	// Scripts decides whether inscribed HTML and SVG may execute.
	//
	// This is the single switch behind recursion (spec R-8.5). With it off,
	// an inscription is a picture; with it on, an inscription can compose
	// itself from other inscriptions at render time, which is the whole
	// p5.js-as-a-shared-library pattern. It is off by default in the
	// zero-value Server and turned on by the flag in cmd/zcribe, so a Server
	// built by hand never silently starts running strangers code.
	Scripts bool

	// ExplorerPreset is the -explorer flag's value, resolved per request
	// rather than at startup: with the default "auto" the answer depends on
	// the chain identifier, and the node has not been asked for it yet when
	// the Server is constructed. Resolving late costs a map lookup and means
	// /info is right on the first request instead of after a restart.
	ExplorerPreset string

	// admin holds the panel's auth state. Unset (the zero value) until
	// EnableAdmin is called, which is what makes every /admin/api/ route
	// answer 503 rather than accept requests against a password that was
	// never configured.
	admin *adminAuth

	// faucet holds the EVM faucet's signing key and terms. Nil until
	// EnableEVMFaucet is called, which is what makes POST /evm/faucet answer
	// 501 rather than sign with a key nobody configured.
	faucet *evmFaucet
}

func New(st *store.Store, ix *indexer.Indexer, contentOrigin string, log *slog.Logger) *Server {
	return &Server{st: st, ix: ix, log: log, ContentOrigin: contentOrigin,
		EVMMaxStale: defaultEVMMaxStale, EVMCheckpointEvery: defaultEVMCheckpointEvery}
}

// Routes returns the metadata API. It does not serve content bytes.
func (s *Server) Routes(ui http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", s.handleInfo)
	mux.HandleFunc("GET /assets", s.handleAssets)
	mux.HandleFunc("GET /asset/{id}", s.handleAsset)
	mux.HandleFunc("GET /asset/{id}/holders", s.handleHolders)
	mux.HandleFunc("GET /asset/{id}/refs", s.handleRefs)
	mux.HandleFunc("GET /asset/{id}/transfers", s.handleTransfers)
	mux.HandleFunc("GET /address/{addr}", s.handleAddress)
	mux.HandleFunc("GET /collections", s.handleCollections)
	mux.HandleFunc("GET /collection/{id}", s.handleCollection)
	mux.HandleFunc("GET /dispensers", s.handleDispensers)
	mux.HandleFunc("GET /dispenser/{id}", s.handleDispenser)
	mux.HandleFunc("GET /momentum/{height}", s.handleMomentum)
	mux.HandleFunc("GET /stateroot/{height}", s.handleStateRoot)
	mux.HandleFunc("GET /state", s.handleProjectionState)
	s.evmRoutes(mux)
	if ui != nil {
		mux.Handle("/", ui)
	}

	// The admin API is mounted outside withCORS, deliberately: it is
	// same-origin only, authenticated by a cookie the browser will not
	// attach cross-origin, and it must never answer with
	// Access-Control-Allow-Origin: * the way the public, unauthenticated API
	// does. /admin (the panel's static shell) still falls through to ui
	// above like any other asset — only /admin/api/ needs its own CORS
	// posture, because that is the part an authenticated cookie rides on.
	top := http.NewServeMux()
	top.Handle("/admin/api/", s.adminRoutes())
	top.Handle("/", withCORS(mux))
	return top
}

// ContentRoutes returns the content origin: raw inscription bytes, and
// nothing else. Run it on a different port (and, in production, a different
// hostname) from Routes.
func (s *Server) ContentRoutes() http.Handler {
	mux := http.NewServeMux()

	// Content lives at /content/{id}/ — with the trailing slash — and that
	// slash is load-bearing rather than cosmetic. It makes the asset its own
	// base URL, so an inscribed page reaches import slot n with the relative
	// reference "n" and never has to embed a 64-character hash it cannot know
	// at authoring time. /content/{id} redirects to it so no link breaks, and
	// so a page can never end up with the wrong base.
	mux.HandleFunc("GET /content/{id}", s.redirectToCanonical)
	mux.HandleFunc("GET /content/{id}/{$}", s.handleContent)
	mux.HandleFunc("GET /content/{id}/{slot}", s.handleRefSlot)

	// The recursion endpoints. They live on the content origin, not the API
	// origin, because an inscribed page can only reach same-origin URLs with a
	// relative reference — and a relative reference is the only kind an
	// inscription can contain without hard-coding where this indexer is
	// deployed.
	mux.HandleFunc("GET /r/momentum", s.handleRecursiveMomentum)
	mux.HandleFunc("GET /r/asset/{id}", s.handleRecursiveAsset)
	mux.HandleFunc("GET /r/refs/{id}", s.handleRefs)
	mux.HandleFunc("GET /r/children/{id}", s.handleRecursiveChildren)
	return withCORS(mux)
}

// withCORS allows a browser front end on another port to read the API. The
// content origin gets the same treatment so an <img> or fetch from the UI
// works; the isolation that matters is the origin the inscribed script runs
// in, which CORS does not affect.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// --- /info ---

type infoResponse struct {
	Protocol          string   `json:"protocol"`
	Prefix            string   `json:"prefix"`
	ProtocolVersion   uint64   `json:"protocolVersion"`
	IndexerVersion    string   `json:"indexerVersion"`
	SyncHeight        uint64   `json:"syncHeight"`
	NodeHeight        uint64   `json:"nodeHeight"`
	StartHeight       uint64   `json:"startHeight"`
	ConfirmationDepth uint64   `json:"confirmationDepth"`
	StateRoot         string   `json:"stateRoot"`
	Events            int64    `json:"events"`
	ContentOrigin     string   `json:"contentOrigin"`
	ScriptsEnabled    bool     `json:"scriptsEnabled"`
	SnapshotInterval  uint64   `json:"snapshotInterval"`
	SnapshotRetain    uint64   `json:"snapshotRetain"`
	MaxRefs           int      `json:"maxRefs"`
	MaxBlockData      int      `json:"maxBlockData"`
	MaxBlockDataPoW   int      `json:"maxBlockDataPoW"`
	MaxAssetParts     int      `json:"maxAssetParts"`
	AllowedMimes      []string `json:"allowedMimes"`

	// DBSizeBytes is the database's on-disk footprint (main file + WAL +
	// shared-memory index); ContentBytes is just the inscribed content within
	// it — the raw data volume, as distinct from the event log and indexes
	// that also live in DBSizeBytes.
	DBSizeBytes  int64 `json:"dbSizeBytes"`
	ContentBytes int64 `json:"contentBytes"`

	// SyncDurationSeconds is (nodeHeight-startHeight) at the indexer's
	// measured momentums/sec, i.e. approximately how long fetching and
	// indexing this whole chain takes at the observed rate. Omitted until
	// enough momentums have been processed to measure a rate.
	SyncDurationSeconds float64 `json:"syncDurationSeconds,omitempty"`

	// ChainID is the network the indexed node is on, so a viewer can say
	// which chain it is looking at rather than assuming mainnet.
	ChainID uint64 `json:"chainId,omitempty"`

	// Explorer is the permalink templates for this network. A zero value
	// means "no explorer known", and the viewer renders hashes as plain text.
	Explorer Explorer `json:"explorer"`
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildInfo())
}

// buildInfo is shared with the admin status endpoint, which reports the same
// facts to the operator alongside things /info deliberately does not publish.
func (s *Server) buildInfo() infoResponse {
	cfg := s.ix.Config()
	syncHeight, nodeHeight := s.ix.SyncHeight(), s.ix.NodeHeight()
	root, _ := s.st.StateRoot(syncHeight)
	events, _ := s.st.EventCount()
	chain := s.ix.ChainID()
	dbSize, _ := s.st.Size()
	contentBytes, _ := s.st.ContentBytes()
	// An unknown preset was already rejected at startup, so a failure here
	// can only mean the chain is one no preset covers — which is exactly the
	// zero Explorer this returns.
	explorer, _ := ResolveExplorer(s.ExplorerPreset, chain)

	// Extrapolated from the measured rate over the full range this indexer is
	// configured to cover, not just what is left — "how long does syncing
	// this chain take", not "how much longer". A rate below startHeight
	// (never observed yet) reports no estimate rather than a bogus one.
	var syncDuration float64
	target := nodeHeight
	if target < syncHeight {
		target = syncHeight
	}
	if rate := s.ix.SyncRate(); rate > 0 && target > cfg.StartHeight {
		syncDuration = float64(target-cfg.StartHeight) / rate
	}

	return infoResponse{
		Protocol:            "zenoglyphs",
		Prefix:              protocol.Prefix,
		ProtocolVersion:     protocol.Version,
		IndexerVersion:      Version,
		SyncHeight:          syncHeight,
		NodeHeight:          nodeHeight,
		StartHeight:         cfg.StartHeight,
		ConfirmationDepth:   cfg.ConfirmationDepth,
		StateRoot:           root,
		Events:              events,
		ContentOrigin:       s.ContentOrigin,
		ScriptsEnabled:      s.Scripts,
		SnapshotInterval:    cfg.SnapshotInterval,
		SnapshotRetain:      cfg.SnapshotRetain,
		MaxRefs:             protocol.MaxRefs,
		MaxBlockData:        protocol.MaxDataLength,
		MaxBlockDataPoW:     protocol.MaxDataLengthPoW,
		MaxAssetParts:       protocol.MaxAssetParts,
		AllowedMimes:        protocol.AllowedMimes(),
		DBSizeBytes:         dbSize,
		ContentBytes:        contentBytes,
		SyncDurationSeconds: syncDuration,
		ChainID:             chain,
		Explorer:            explorer,
	}
}

// --- assets ---

type assetJSON struct {
	ID           string          `json:"id"`
	Number       int64           `json:"number"`
	Issuer       string          `json:"issuer"`
	Owner        string          `json:"owner"`
	Collection   string          `json:"collection,omitempty"`
	Name         string          `json:"name,omitempty"`
	Description  string          `json:"description,omitempty"`
	Supply       string          `json:"supply"`
	Decimals     uint64          `json:"decimals"`
	Mime         string          `json:"mime"`
	Class        string          `json:"class"`
	Attributes   json.RawMessage `json:"attributes,omitempty"`
	Extensions   json.RawMessage `json:"extensions,omitempty"`
	Size         int             `json:"size"`
	Parts        int             `json:"parts"`
	Height       uint64          `json:"momentum"`
	Index        int             `json:"momentumIndex"`
	Timestamp    int64           `json:"timestamp"`
	ContentURL   string          `json:"contentUrl"`
	DataWithheld bool            `json:"dataWithheld,omitempty"`

	// Two confirmation levels, because they answer different questions
	// (spec R-6.3).
	//
	//	status "soft"  this indexer has parsed the block and applied it.
	//	               Everything the API returns is at least this. It means
	//	               the inscription exists and is renderable now, and it
	//	               says nothing about whether it will survive a reorg.
	//	status "full"  ConfirmationDepth momentums sit above it. The
	//	               inscription number is settled and the mint is as final
	//	               as anything on the chain.
	//
	// Confirmations counts the inscription momentum itself, so a mint in the
	// momentum the indexer just processed reports 1, not 0.
	Status        string `json:"status"`
	Confirmations uint64 `json:"confirmations"`
	Confirmed     bool   `json:"confirmed"`

	// Refs is the composite import list in slot order (spec R-4.7). Slot n is
	// served at ContentURL + n.
	Refs []string `json:"refs,omitempty"`
}

// confirmationsFor counts how many momentums this indexer has processed at or
// above an inscription height. It is deliberately measured against the
// indexer's own sync height rather than the node frontier: an indexer must not
// report a depth it has not itself verified.
func (s *Server) confirmationsFor(height uint64) uint64 {
	sync := s.ix.SyncHeight()
	if sync < height {
		return 0
	}
	return sync - height + 1
}

func (s *Server) assetToJSON(a store.Asset) assetJSON {
	cfg := s.ix.Config()
	confirmations := s.confirmationsFor(a.Height)
	confirmed := confirmations >= cfg.ConfirmationDepth
	status := "soft"
	if confirmed {
		status = "full"
	}

	out := assetJSON{
		ID: a.ID, Number: a.Number, Issuer: a.Issuer, Owner: a.Owner,
		Collection: a.CollectionID, Name: a.Name, Description: a.Description,
		Supply: strconv.FormatUint(a.Supply, 10), Decimals: a.Decimals,
		Mime: a.Mime, Class: string(protocol.ClassOf(a.Mime)),
		Size: a.Size, Parts: a.PartCount,
		Height: a.Height, Index: a.MomentumIndex, Timestamp: a.Timestamp,
		Status: status, Confirmations: confirmations, Confirmed: confirmed,
		ContentURL: s.contentURL(a.ID),
	}
	if refs, err := s.st.RefsOf(a.ID); err == nil && len(refs) > 0 {
		out.Refs = refs
	}
	if len(a.Attributes) > 0 {
		// Attributes arrive as CBOR and are re-encoded to JSON only here, at
		// the serving edge. The CBOR stays canonical in the store because it
		// is what the block committed to.
		if j, err := cborToJSON(a.Attributes); err == nil {
			out.Attributes = j
		}
	}
	// Extensions are surfaced to clients but never interpreted here: they
	// are consensus-inert by construction (spec R-2.9), so a client may do
	// whatever it likes with them without any indexer having to agree.
	if len(a.Ext) > 0 {
		if j, err := cborToJSON(a.Ext); err == nil {
			out.Extensions = j
		}
	}
	if mod, err := s.st.Moderation(a.ID); err == nil && !mod.DataPermitted {
		out.DataWithheld = true
	}
	return out
}

func cborToJSON(raw []byte) ([]byte, error) {
	var v any
	if err := protocol.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.Marshal(normalize(v))
}

// normalize converts CBOR's map[any]any into something encoding/json accepts.
func normalize(v any) any {
	switch t := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[fmt.Sprint(k)] = normalize(val)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalize(val)
		}
		return out
	case []byte:
		return hex.EncodeToString(t)
	default:
		return v
	}
}

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	assets, total, err := s.st.ListAssets(store.AssetFilter{
		Owner:      q.Get("owner"),
		Issuer:     q.Get("issuer"),
		Collection: q.Get("collection"),
		Mime:       q.Get("mime"),
		Limit:      limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]assetJSON, 0, len(assets))
	for _, a := range assets {
		out = append(out, s.assetToJSON(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": total, "count": len(out), "offset": offset, "assets": out,
	})
}

// lookupAsset resolves either a 64-hex asset id or an inscription number.
func (s *Server) lookupAsset(id string) (*store.Asset, error) {
	if n, err := strconv.ParseInt(id, 10, 64); err == nil && !strings.ContainsAny(id, "abcdefABCDEF") {
		return s.st.AssetByNumber(n)
	}
	return s.st.Asset(strings.ToLower(id))
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	a, err := s.lookupAsset(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if a == nil {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	mod, err := s.st.Moderation(a.ID)
	if err == nil && !mod.MetadataPermitted {
		writeErr(w, http.StatusGone, "withheld by indexer policy")
		return
	}
	writeJSON(w, http.StatusOK, s.assetToJSON(*a))
}

func (s *Server) handleHolders(w http.ResponseWriter, r *http.Request) {
	a, err := s.lookupAsset(r.PathValue("id"))
	if err != nil || a == nil {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	holders, err := s.st.HoldersOf(a.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A dispenser's stock sits in this table under the dispenser's id while
	// it is for sale, so one row in every holders list can be a block hash
	// rather than an account. It is rendered as the address that names it
	// (spec R-8.6) with the id alongside, never as a raw hash.
	escrow, err := s.escrowParties(a.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]string, 0, len(holders))
	for _, h := range holders {
		address, dispenser := party(escrow, h.Address)
		row := map[string]string{
			"address": address,
			"amount":  strconv.FormatUint(h.Amount, 10),
		}
		if dispenser != "" {
			row["dispenser"] = dispenser
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"holders": out})
}

func (s *Server) handleTransfers(w http.ResponseWriter, r *http.Request) {
	a, err := s.lookupAsset(r.PathValue("id"))
	if err != nil || a == nil {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	// The feed is assembled rather than read: issuance and the escrow legs
	// of a dispenser move balances without writing a transfer row, and a
	// provenance record with silent holes is worse than none. See
	// provenance.go for why they are reconstructed here and not written by
	// the indexer.
	out, err := s.transferFeed(a, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transfers": out})
}

// --- /content ---

// handleContent serves the inscribed bytes.
//
// Every header here is load-bearing:
//
//   - Content-Security-Policy: sandbox strips the response of same-origin
//     privileges, so an inscribed page cannot reach this origin's storage or
//     network even though it is served from it.
//   - X-Content-Type-Options: nosniff stops a browser from reinterpreting a
//     text/plain inscription as HTML.
//   - Content-Disposition: inline with no filename keeps a hostile name out
//     of the download path.
//
// contentURL is the canonical, trailing-slash URL for an asset's bytes. Every
// link the API hands out uses it, so an inscribed page always renders with its
// own id as the base and relative import slots resolve.
func (s *Server) contentURL(id string) string {
	return s.ContentOrigin + "/content/" + id + "/"
}

// redirectToCanonical sends /content/{id} to /content/{id}/.
//
// Permanent, because the mapping can never change: an id is a block hash. The
// redirect exists so that a page loaded through the short form still ends up
// with the trailing-slash base URL, without which a relative import slot would
// resolve against /content/ and silently fetch the wrong asset.
func (s *Server) redirectToCanonical(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("id"))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.Redirect(w, r, "/content/"+id+"/", http.StatusMovedPermanently)
}

// handleRefSlot resolves import slot n of a composite (spec R-4.7.4).
//
// It redirects rather than serving the bytes, and that is deliberate: one
// content blob keeps one canonical URL, so a CDN caches it once however many
// composites import it, and a moderation policy on the target applies at the
// target rather than having to be mirrored onto every importer.
func (s *Server) handleRefSlot(w http.ResponseWriter, r *http.Request) {
	slot, err := strconv.Atoi(r.PathValue("slot"))
	if err != nil || slot < 0 {
		writeErr(w, http.StatusBadRequest, "slot must be a non-negative integer")
		return
	}
	a, err := s.lookupAsset(r.PathValue("id"))
	if err != nil || a == nil {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	refs, err := s.st.RefsOf(a.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if slot >= len(refs) {
		writeErr(w, http.StatusNotFound,
			fmt.Sprintf("asset declares %d import slots; %d was asked for", len(refs), slot))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.Redirect(w, r, "/content/"+refs[slot]+"/", http.StatusMovedPermanently)
}

func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	a, err := s.lookupAsset(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if a == nil {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	mod, err := s.st.Moderation(a.ID)
	if err == nil && (!mod.DataPermitted || !mod.MetadataPermitted) {
		writeErr(w, http.StatusGone, "withheld by indexer policy")
		return
	}

	w.Header().Set("Content-Type", a.Mime)
	w.Header().Set("Content-Security-Policy", s.contentCSP())
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")

	// An inscription's bytes are immutable: the id is the block hash, and the
	// block hash commits to the data. There is no revision to invalidate, so
	// this is one of the rare cases where an immutable, year-long cache is
	// simply correct rather than optimistic.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+a.ID+`"`)

	// ServeContent rather than Write: it handles Range requests, which is what
	// makes an inscribed audio or video file seekable instead of forcing a
	// full download before playback starts. It honours the Content-Type
	// already set above rather than sniffing, which matters here because
	// sniffing is exactly what nosniff exists to prevent.
	http.ServeContent(w, r, "", time.Unix(a.Timestamp, 0), bytes.NewReader(a.Content))
}

// contentCSP is the policy every content response carries.
//
// Three things are doing work here.
//
// sandbox strips the response of same-origin privileges. With Scripts off it
// takes no token at all and nothing executes — the original behaviour. With
// Scripts on it takes allow-scripts and deliberately NOT allow-same-origin:
// the document runs, in an opaque origin, so one inscription script can never
// reach another inscription frame's DOM or storage. Cross-origin reads still
// work, because this origin sends Access-Control-Allow-Origin: * — which is
// why recursion never needed same-origin in the first place.
//
// Every fetch directive is pinned to the content origin. An inscription may
// therefore compose itself out of other inscriptions and out of nothing else:
// no CDN, no analytics, no tracking pixel, no author-controlled URL that can
// be repointed after the mint. That is what makes "this artwork is on the
// chain" a claim about the rendered result rather than about one file.
//
// 'unsafe-inline' is unavoidable — a self-contained inscription has nowhere to
// put a script but inline — and is bounded by the fetch directives around it.
//
// 'wasm-unsafe-eval' is what lets an inscription instantiate WebAssembly. It is
// deliberately the narrow token and not 'unsafe-eval': verified in Chrome
// against this exact policy, it permits WebAssembly.instantiate while eval()
// and new Function() both still throw EvalError. Without it, WebAssembly
// .validate() still returns true — it is only a parse — and instantiate()
// fails with a CompileError, which is a confusing way to discover the policy.
//
// It grants no new reach. A module still arrives through the pinned fetch
// directives below, so an inscription composes itself out of other
// inscriptions and out of nothing else, exactly as before.
func (s *Server) contentCSP() string {
	o := s.ContentOrigin
	if o == "" {
		o = "'self'"
	}
	sandbox := "sandbox"
	if s.Scripts {
		sandbox = "sandbox allow-scripts"
	}
	return strings.Join([]string{
		sandbox,
		"default-src " + o,
		"script-src " + o + " 'unsafe-inline' 'wasm-unsafe-eval'",
		"style-src " + o + " 'unsafe-inline'",
		"img-src " + o + " data: blob:",
		"media-src " + o + " data: blob:",
		"font-src " + o + " data:",
		"connect-src " + o,
		"frame-src " + o,
		"base-uri 'none'",
		"form-action 'none'",
	}, "; ")
}

// --- composite imports ---

// handleRefs serves both directions of the import graph: what this asset
// declares it is built from, and what declares itself built from this asset.
// The reverse edge is the half a client cannot compute without reading every
// mint on the chain.
func (s *Server) handleRefs(w http.ResponseWriter, r *http.Request) {
	a, err := s.lookupAsset(r.PathValue("id"))
	if err != nil || a == nil {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	if mod, err := s.st.Moderation(a.ID); err == nil && !mod.MetadataPermitted {
		writeErr(w, http.StatusGone, "withheld by indexer policy")
		return
	}
	refs, err := s.st.RefsOf(a.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	back, err := s.st.ReferencedBy(a.ID, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(refs))
	for i, id := range refs {
		e := map[string]any{"slot": i, "id": id, "url": s.contentURL(id)}
		// A ref names a block hash and is never validated at mint time, so a
		// dangling edge is an ordinary outcome rather than an error. Saying
		// which ones resolve is the indexer's job; refusing them is not.
		target, terr := s.st.Asset(id)
		mod, merr := s.st.Moderation(id)
		switch {
		case merr == nil && !mod.MetadataPermitted:
			// A blocked target is reported as blocked, not as missing. The
			// distinction matters to whoever is looking at a broken
			// composite: "this operator will not serve it" and "nobody has
			// ever inscribed it" call for different next steps.
			e["resolved"] = false
			e["withheld"] = true
		case terr == nil && target != nil:
			e["resolved"] = true
			e["mime"] = target.Mime
			e["name"] = target.Name
		default:
			e["resolved"] = false
		}
		out = append(out, e)
	}
	if back == nil {
		back = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": a.ID, "refs": out, "referencedBy": back,
		"maxRefs": protocol.MaxRefs, "maxDepth": protocol.MaxRefDepth,
	})
}

// --- recursion endpoints (content origin) ---

// handleRecursiveMomentum is what an inscription reads to know when it is.
// ord exposes /r/blockheight and /r/blocktime for the same purpose: it is how
// a generative piece can vary with the chain instead of with the clock on the
// machine rendering it.
func (s *Server) handleRecursiveMomentum(w http.ResponseWriter, r *http.Request) {
	h := s.ix.SyncHeight()
	m, _ := s.st.Momentum(h)
	out := map[string]any{"height": h, "nodeHeight": s.ix.NodeHeight()}
	if m != nil {
		out["hash"] = m.Hash
		out["timestamp"] = m.Timestamp
		out["stateRoot"] = m.StateRoot
	}
	// The tip moves; nothing here may be cached the way content is.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRecursiveAsset(w http.ResponseWriter, r *http.Request) {
	a, err := s.lookupAsset(r.PathValue("id"))
	if err != nil || a == nil {
		writeErr(w, http.StatusNotFound, "asset not found")
		return
	}
	if mod, err := s.st.Moderation(a.ID); err == nil && !mod.MetadataPermitted {
		writeErr(w, http.StatusGone, "withheld by indexer policy")
		return
	}
	writeJSON(w, http.StatusOK, s.assetToJSON(*a))
}

// handleRecursiveChildren lists a collection's members, so a composite can
// render a whole set — a contact sheet, a trait table — without the author
// having to inscribe the member list separately and keep it in step.
func (s *Server) handleRecursiveChildren(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("id"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	assets, total, err := s.st.ListAssets(store.AssetFilter{
		Collection: id, Limit: limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ids := make([]string, 0, len(assets))
	for _, a := range assets {
		ids = append(ids, a.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"collection": id, "total": total, "offset": offset, "ids": ids,
	})
}

// --- address, collections, dispensers ---

func (s *Server) handleAddress(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("addr")

	// An escrow address is a display form of a dispenser id, and the balance
	// table is keyed by the id. Resolving it back means the address behaves
	// like an address wherever it is rendered — a holders row that says
	// z1zen0glyphxescr0w… links to a page that can answer for it, instead of one
	// that says the address holds nothing. The scan is over dispensers and
	// only ever runs for a string already in the namespace, so nobody else's
	// lookup pays for it.
	key, dispenser := addr, ""
	if protocol.IsEscrowAddress(addr) {
		if id, err := s.dispenserForEscrow(addr); err == nil && id != "" {
			key, dispenser = id, id
		}
	}

	holdings, err := s.st.HoldingsOf(key)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type holding struct {
		Asset  assetJSON `json:"asset"`
		Amount string    `json:"amount"`
	}
	out := make([]holding, 0, len(holdings))
	for _, h := range holdings {
		a, err := s.st.Asset(h.AssetID)
		if err != nil || a == nil {
			continue
		}
		if mod, err := s.st.Moderation(a.ID); err == nil && !mod.MetadataPermitted {
			continue
		}
		out = append(out, holding{
			Asset:  s.assetToJSON(*a),
			Amount: strconv.FormatUint(h.Amount, 10),
		})
	}
	resp := map[string]any{"address": addr, "holdings": out}
	if dispenser != "" {
		resp["dispenser"] = dispenser
	}
	writeJSON(w, http.StatusOK, resp)
}

// dispenserForEscrow finds the dispenser an escrow address names.
//
// The address carries 105 free bits and a dispenser id needs 256, so it
// cannot be inverted — it is matched instead, by deriving the address of
// every dispenser and comparing. That is the cost of keeping the ledger key
// unchanged (see internal/protocol/address.go), and it is paid here, once,
// on a lookup nobody makes by accident.
func (s *Server) dispenserForEscrow(addr string) (string, error) {
	ds, err := s.st.ListDispensers(false)
	if err != nil {
		return "", err
	}
	for _, d := range ds {
		if protocol.EscrowAddress(d.ID) == addr {
			return d.ID, nil
		}
	}
	return "", nil
}

func (s *Server) handleCollections(w http.ResponseWriter, r *http.Request) {
	cols, err := s.st.ListCollections()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": cols})
}

func (s *Server) handleCollection(w http.ResponseWriter, r *http.Request) {
	c, err := s.st.Collection(strings.ToLower(r.PathValue("id")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "collection not found")
		return
	}
	assets, total, err := s.st.ListAssets(store.AssetFilter{Collection: c.ID, Limit: 200})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]assetJSON, 0, len(assets))
	for _, a := range assets {
		out = append(out, s.assetToJSON(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"collection": c, "total": total, "assets": out,
	})
}

func (s *Server) handleDispensers(w http.ResponseWriter, r *http.Request) {
	open := r.URL.Query().Get("open") != "false"
	ds, err := s.st.ListDispensers(open)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dispensers": dispensersJSON(ds)})
}

func (s *Server) handleDispenser(w http.ResponseWriter, r *http.Request) {
	d, err := s.st.Dispenser(strings.ToLower(r.PathValue("id")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if d == nil {
		writeErr(w, http.StatusNotFound, "dispenser not found")
		return
	}
	writeJSON(w, http.StatusOK, dispensersJSON([]store.Dispenser{*d})[0])
}

func dispensersJSON(ds []store.Dispenser) []map[string]any {
	out := make([]map[string]any, 0, len(ds))
	for _, d := range ds {
		out = append(out, map[string]any{
			"id": d.ID, "issuer": d.Issuer, "asset": d.AssetID,
			"remaining": strconv.FormatUint(d.Remaining, 10),
			"price":     strconv.FormatUint(d.Price, 10),
			"zts":       d.ZTS, "cancelled": d.Cancelled,
			"momentum": d.Height, "timestamp": d.Timestamp,
		})
	}
	return out
}

// --- cross-indexer agreement ---

// handleMomentum publishes what this indexer believes about one height. Two
// indexers agreeing on stateRoot(H) agree on the entire history to H; when
// they disagree, a binary search over this endpoint finds the exact divergent
// momentum in about twenty queries.
func (s *Server) handleMomentum(w http.ResponseWriter, r *http.Request) {
	h, err := strconv.ParseUint(r.PathValue("height"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad height")
		return
	}
	m, err := s.st.Momentum(h)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if m == nil {
		writeErr(w, http.StatusNotFound, "height not indexed")
		return
	}
	txs, err := s.st.TxHashesAt(h)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if txs == nil {
		txs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"height": m.Height, "hash": m.Hash, "previousHash": m.PreviousHash,
		"timestamp": m.Timestamp, "txs": txs, "stateRoot": m.StateRoot,
	})
}

// handleProjectionState publishes the digest of everything this indexer has
// projected. The state root proves two indexers agree about which events
// applied; this proves they agree about what those events produced. A
// projection bug on one side leaves the applied set identical and the state
// roots equal, so it is the only check that catches one.
//
// It is a diagnostic, never consensus: no protocol rule refers to it, and
// moderation is excluded so two operators with different policies still match.
func (s *Server) handleProjectionState(w http.ResponseWriter, r *http.Request) {
	digest, err := s.st.ProjectionDigest()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	root, _ := s.st.StateRoot(s.ix.SyncHeight())
	writeJSON(w, http.StatusOK, map[string]any{
		"syncHeight": s.ix.SyncHeight(), "stateRoot": root,
		"projectionDigest": digest,
	})
}

func (s *Server) handleStateRoot(w http.ResponseWriter, r *http.Request) {
	h, err := strconv.ParseUint(r.PathValue("height"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad height")
		return
	}
	root, err := s.st.StateRoot(h)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if root == "" {
		writeErr(w, http.StatusNotFound, "height not indexed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"height": h, "stateRoot": root})
}
