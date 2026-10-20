package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/evm"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// The derived EVM chain the /evm routes serve.
//
// # Following the tail
//
// A full derivation is O(history), so it happens rarely, and from then on the
// cache follows the tail: before a request reads, whatever EVM inscriptions
// the indexer has added since the last catch-up are applied, in canonical
// order, onto the chain already held. That is sound because canonical order
// is append-only, with two exceptions, a projection reset and a checkpoint
// restore (every reorg and every reindex). Both bump
// store.ProjectionGeneration in the same transaction as the rows they
// rewrite, and a changed generation means a rebuild.
//
// Until 2026-09-11 this was a full replay whenever the *count* of EVM
// inscriptions changed, with every inscription's content loaded on every
// request to count them; at 8 k transactions and 128 pollers /evm/overview
// took 6 s (research/evm-benchmark.md). The count was also the wrong key: a
// reorg that swaps one EVM inscription for another leaves it unchanged.
//
// # Not asking when nothing can have changed
//
// A catch-up is two small SQLite reads, but every request made them, one at a
// time, and at ~500 req/s that queue was the whole latency. The indexer in
// this process bumps Progress after every commit, so a request that sees the
// value the last catch-up saw skips the store entirely. What Progress cannot
// see is another process — `zcribe reindex` against the same database — so
// the skip is also bounded by EVMMaxStale: after that, the generation is
// read again whatever Progress says.
//
// # Checkpoints
//
// A rebuild used to mean a replay from genesis: every restart, every reorg.
// Receipts are now persisted as they are produced and the derived state is
// checkpointed every EVMCheckpointEvery inscriptions (store/evmstore.go), so
// a rebuild restores the newest checkpoint and applies only what came after
// it. The two tables are ordinary projections — truncated and reset with the
// assets they derive from — so any checkpoint still in the store belongs to
// the current history. One is refused, and replaced by a replay, if it is
// from other rules (evm.DerivationVersion), another chain, is missing
// receipts, or restores to a root other than the one it was written with.
//
// # Locking
//
// mu is a read/write lock over the chain. A handler holds the read side for
// its whole run (evmRead), because the tail is applied in place and a reader
// must never see half of it. The write side is taken only to apply a tail —
// microseconds per transaction — or to swap in a rebuilt chain, which is
// built outside it. upd serialises catching up, rebuilding and persisting,
// so a burst of requests after a momentum applies its tail once; everything
// under upd may read the chain without mu, because only upd's holder writes.
//
// evm.State's exported getters are pure, and evm.Chain.View copies before it
// executes anything, so no handler writes to the shared state; that is what
// makes the read side safe to share.
type evmCache struct {
	mu    sync.RWMutex
	upd   sync.Mutex
	chain *evm.Chain

	// Where the chain has read up to: the canonical position of the last
	// inscription row consumed (applied or skipped; index -1 is "none"), and
	// the projection generation it was read under.
	height uint64
	index  int
	gen    int64

	// Derived once per catch-up rather than per request.
	root              string
	contracts, tokens int

	// persisted is whether every receipt up to the read position is in the
	// store for this generation — the condition for a checkpoint a restart
	// can trust. It goes false on any failed write and stays false until the
	// next rebuild re-establishes it. Guarded by upd.
	persisted       bool
	sinceCheckpoint int

	// For /evm/overview. contentBytes is the inscribed volume the chain is
	// built from; rebuildTime is the last rebuild and applyTime the last
	// tail; restoredFrom says whether the last rebuild resumed a checkpoint.
	contentBytes int64
	rebuildTime  time.Duration
	applyTime    time.Duration
	rebuilds     int
	restoredFrom string
	checkpointAt string

	// The background check (VerifyEVM).
	verifiedAt time.Time
	verifyErr  string

	// The fast path's memory, read without locks.
	ready        atomic.Bool
	seenProgress atomic.Uint64
	checkedAt    atomic.Int64 // unix nanoseconds
}

// Defaults for the Server fields that tune the cache.
const (
	defaultEVMMaxStale        = time.Second
	defaultEVMCheckpointEvery = 1024
)

// evmRead brings the derived chain up to date and returns it under the read
// lock. The caller must call done when it has finished reading — typically
// `defer done()` — and must not keep the chain past that: the next tail is
// applied to it in place.
func (s *Server) evmRead() (c *evm.Chain, done func(), err error) {
	if err := s.evmCatchUp(); err != nil {
		return nil, nil, err
	}
	s.evm.mu.RLock()
	return s.evm.chain, s.evm.mu.RUnlock, nil
}

// evmFresh reports that the last catch-up is still current: the in-process
// indexer has committed nothing since, and it was recent enough that a
// rewrite from another process would already have been looked for.
func (s *Server) evmFresh() bool {
	if s.ix == nil || s.EVMMaxStale <= 0 || !s.evm.ready.Load() {
		return false
	}
	if s.ix.Progress() != s.evm.seenProgress.Load() {
		return false
	}
	return time.Since(time.Unix(0, s.evm.checkedAt.Load())) < s.EVMMaxStale
}

// evmCatchUp applies the EVM inscriptions indexed since the last call, or
// rebuilds if projection history has been rewritten since.
func (s *Server) evmCatchUp() error {
	if s.evmFresh() {
		return nil
	}
	s.evm.upd.Lock()
	defer s.evm.upd.Unlock()
	if s.evmFresh() {
		return nil // caught up by whoever held upd before us
	}
	// Read before the store, so a commit that lands during the catch-up
	// leaves Progress ahead of what is recorded and the next request looks.
	var progress uint64
	if s.ix != nil {
		progress = s.ix.Progress()
	}
	checked := time.Now()
	if err := s.evmCatchUpLocked(); err != nil {
		return err
	}
	s.evm.seenProgress.Store(progress)
	s.evm.checkedAt.Store(checked.UnixNano())
	s.evm.ready.Store(true)
	return nil
}

func (s *Server) evmCatchUpLocked() error {
	e := &s.evm
	gen, err := s.st.ProjectionGeneration()
	if err != nil {
		return err
	}
	if e.chain == nil || gen != e.gen || s.evmChainID() != e.chain.ChainID {
		return s.evmRebuild(gen, false)
	}

	tail, err := s.st.AssetsOfMimeCarriedAfter(evm.Mime, e.height, e.index)
	if err != nil {
		return err
	}
	if len(tail) == 0 {
		return nil
	}
	// A rewrite that landed between the generation read and the tail read
	// would hand us rows from a different history. Read the generation again
	// and rebuild instead of appending across the seam.
	if again, err := s.st.ProjectionGeneration(); err != nil {
		return err
	} else if again != gen {
		return s.evmRebuild(again, false)
	}

	start := time.Now()
	e.mu.Lock()
	before := len(e.chain.Receipts)
	if err := e.chain.Apply(inscriptionsOf(tail)); err != nil {
		e.mu.Unlock()
		return err
	}
	e.applyTime = time.Since(start)
	e.contentBytes += contentOf(tail)
	e.note(tail)
	e.mu.Unlock()

	s.evmPersist(tail, before)
	return nil
}

// evmRebuild replaces the chain: from the newest usable checkpoint plus the
// inscriptions after it, or, failing that or when fromScratch is set, by a
// replay from genesis. Readers keep the old chain until the new one is
// swapped in. Callers hold upd.
func (s *Server) evmRebuild(gen int64, fromScratch bool) error {
	e := &s.evm
	start := time.Now()
	chainID := s.evmChainID()

	var (
		c         *evm.Chain
		tail      []store.CarriedAsset
		height    uint64
		index     = -1
		from      = "genesis"
		persisted bool
	)
	if !fromScratch {
		if cp, restored := s.evmFromCheckpoint(chainID); restored != nil {
			c, height, index = restored, cp.Height, cp.MomentumIndex
			from = fmt.Sprintf("checkpoint %d:%d", cp.Height, cp.MomentumIndex)
			persisted = true // receipts up to the checkpoint are in the store
		}
	}
	var err error
	if c != nil {
		if tail, err = s.st.AssetsOfMimeCarriedAfter(evm.Mime, height, index); err != nil {
			return err
		}
	} else {
		if tail, err = s.st.AssetsOfMimeCarried(evm.Mime); err != nil {
			return err
		}
		c = evm.NewChain(chainID)
		// A replay from genesis replaces whatever is persisted: it may be from
		// other rules, or be the checkpoint a divergence came from. Not while
		// the network is unknown, though — clearing then would throw away good
		// checkpoints for the chain this turns out to be.
		if chainID != 0 {
			switch err := s.st.ClearEVMDerived(gen); {
			case err == nil:
				persisted = true
			case !errors.Is(err, store.ErrStaleGeneration) && s.log != nil:
				s.log.Warn("evm: could not clear persisted derivation", "err", err)
			}
		}
	}
	before := len(c.Receipts)
	if err := c.Apply(inscriptionsOf(tail)); err != nil {
		return err
	}
	elapsed := time.Since(start)
	content, err := s.st.MimeContentBytes(evm.Mime)
	if err != nil {
		return err
	}

	e.mu.Lock()
	e.chain, e.gen = c, gen
	e.height, e.index = height, index
	e.note(tail)
	e.contentBytes = content
	e.rebuildTime, e.applyTime = elapsed, 0
	e.rebuilds++
	e.restoredFrom = from
	e.persisted = persisted
	e.sinceCheckpoint = 0
	if from == "genesis" {
		// A restart after this should not have to replay it all again.
		e.sinceCheckpoint = s.evmCheckpointEvery()
	}
	e.mu.Unlock()

	if s.log != nil {
		s.log.Info("evm: derived state rebuilt", "from", from, "generation", gen,
			"applied", len(tail), "receipts", len(c.Receipts), "took", elapsed)
	}
	s.evmPersist(tail, before)
	return nil
}

// evmFromCheckpoint restores the newest checkpoint for these rules and this
// chain, or returns nil — with the reason logged — if there is none or it
// cannot be trusted.
func (s *Server) evmFromCheckpoint(chainID uint64) (*store.EVMCheckpoint, *evm.Chain) {
	if chainID == 0 {
		return nil, nil
	}
	cp, err := s.st.LatestEVMCheckpoint(evm.DerivationVersion, chainID)
	if err != nil || cp == nil {
		if err != nil && s.log != nil {
			s.log.Warn("evm: cannot read checkpoints; replaying from genesis", "err", err)
		}
		return nil, nil
	}
	refuse := func(why string, err error) (*store.EVMCheckpoint, *evm.Chain) {
		if s.log != nil {
			s.log.Warn("evm: checkpoint refused; replaying from genesis", "height", cp.Height,
				"index", cp.MomentumIndex, "why", why, "err", err)
		}
		return nil, nil
	}
	var receipts []evm.Receipt
	if err := s.st.EVMReceiptsUpTo(cp.Height, cp.MomentumIndex, func(row store.EVMReceiptRow) error {
		r, err := evm.UnmarshalReceipt(row.Body)
		if err != nil {
			return err
		}
		receipts = append(receipts, r)
		return nil
	}); err != nil {
		return refuse("unreadable receipts", err)
	}
	c, err := evm.RestoreChain(chainID, cp.State, receipts)
	if err != nil {
		return refuse("does not restore", err)
	}
	if root := c.State.Root(); root != cp.Root {
		return refuse("restores to "+root+", written as "+cp.Root, nil)
	}
	return cp, c
}

// evmPersist writes the receipts produced from rows — those at and after
// index `from` in the chain's receipt list — and takes a checkpoint if one is
// due. Nothing here is state: a failure is logged, stops checkpointing until
// the next rebuild, and costs only the speed of the next restart. Callers
// hold upd.
func (s *Server) evmPersist(rows []store.CarriedAsset, from int) {
	e := &s.evm
	if !e.persisted {
		return
	}
	fail := func(err error) {
		e.persisted = false
		if !errors.Is(err, store.ErrStaleGeneration) && s.log != nil {
			s.log.Warn("evm: could not persist derivation; checkpoints paused until the next rebuild", "err", err)
		}
	}
	at := make(map[string]store.CarriedAsset, len(rows))
	for _, a := range rows {
		at[a.ID] = a
	}
	out := make([]store.EVMReceiptRow, 0, len(e.chain.Receipts)-from)
	for _, r := range e.chain.Receipts[from:] {
		a, ok := at[r.Inscription]
		if !ok {
			fail(fmt.Errorf("receipt %s has no row in the batch that produced it", r.Inscription))
			return
		}
		body, err := evm.MarshalReceipt(r)
		if err != nil {
			fail(err)
			return
		}
		out = append(out, store.EVMReceiptRow{Height: a.Height, MomentumIndex: a.MomentumIndex,
			Inscription: r.Inscription, Body: body})
	}
	if err := s.st.AppendEVMReceipts(e.gen, out); err != nil {
		fail(err)
		return
	}
	e.mu.Lock()
	e.sinceCheckpoint += len(rows)
	due := e.sinceCheckpoint >= s.evmCheckpointEvery() && e.index >= 0
	e.mu.Unlock()
	if !due {
		return
	}
	blob, err := e.chain.MarshalCheckpoint()
	if err != nil {
		fail(err)
		return
	}
	cp := store.EVMCheckpoint{Height: e.height, MomentumIndex: e.index, Version: evm.DerivationVersion,
		ChainID: e.chain.ChainID, Receipts: len(e.chain.Receipts), Root: e.root, State: blob}
	if err := s.st.PutEVMCheckpoint(e.gen, cp, s.evmCheckpointRetain()); err != nil {
		fail(err)
		return
	}
	e.mu.Lock()
	e.sinceCheckpoint = 0
	e.checkpointAt = fmt.Sprintf("%d:%d", cp.Height, cp.MomentumIndex)
	e.mu.Unlock()
}

// note advances the read position past rows just applied and refreshes what
// is derived once per catch-up. Callers hold mu for writing.
func (e *evmCache) note(rows []store.CarriedAsset) {
	if n := len(rows); n > 0 {
		e.height, e.index = rows[n-1].Height, rows[n-1].MomentumIndex
	}
	e.root = e.chain.State.Root()
	e.contracts, e.tokens = 0, 0
	for _, a := range e.chain.State.Accounts() {
		if code := e.chain.State.Code(a); len(code) > 0 {
			e.contracts++
			if evm.LooksLikeERC20(code) {
				e.tokens++
			}
		}
	}
}

func contentOf(rows []store.CarriedAsset) (n int64) {
	for _, a := range rows {
		n += int64(len(a.Content))
	}
	return n
}

func inscriptionsOf(assets []store.CarriedAsset) []evm.Inscription {
	ins := make([]evm.Inscription, 0, len(assets))
	for _, a := range assets {
		ins = append(ins, evm.Inscription{
			ID: a.ID, Issuer: a.Issuer, Mime: a.Mime,
			Height: a.Height, Index: a.MomentumIndex, Timestamp: a.Timestamp,
			Content: a.Content, Ext: a.Ext,
			Recipient: a.Recipient, Amount: a.Amount, TokenStandard: a.TokenStandard,
			BasePlasma: a.BasePlasma, FusedPlasma: a.FusedPlasma, Difficulty: a.Difficulty,
		})
	}
	return ins
}

// evmCheckpointRetain is the reorg depth EVM checkpoints must cover: the
// indexer's own, so a rollback it resumes from a checkpoint the EVM can too.
func (s *Server) evmCheckpointRetain() uint64 {
	if s.ix != nil {
		return s.ix.Config().SnapshotRetain
	}
	return 0
}

func (s *Server) evmCheckpointEvery() int {
	if s.EVMCheckpointEvery > 0 {
		return s.EVMCheckpointEvery
	}
	return defaultEVMCheckpointEvery
}

// evmChainID is the network the derivation runs under. It comes from the
// indexer rather than a config field, so a contract can never be replayed
// against a chain identifier the node did not report; before the indexer has
// heard from the node, it is the identifier this database recorded last time,
// which is what lets a restart resume a checkpoint without waiting for the
// node. The cache rebuilds if it changes.
func (s *Server) evmChainID() uint64 {
	if s.ix != nil {
		if id := s.ix.ChainID(); id != 0 {
			return id
		}
	}
	if v, err := s.st.Meta("chain_id"); err == nil && v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			return id
		}
	}
	return 0
}

// VerifyEVM periodically replays the EVM from genesis and checks that the
// incrementally maintained chain reached the same state root.
//
// Following the tail — and now resuming checkpoints — gave up the self-check
// a full replay performed on every change: a bookkeeping bug (a row applied
// twice, one skipped, a rewrite missed, a checkpoint that restores subtly
// wrong) would accumulate silently. This puts the check back, off the request
// path. A disagreement is logged and answered with a rebuild from genesis
// that also discards the persisted checkpoints, so it is a bug report, not an
// outage. It stops when ctx is done.
func (s *Server) VerifyEVM(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.verifyEVMOnce(); err != nil && s.log != nil {
			s.log.Error("evm: verification failed", "err", err)
		}
	}
}

func (s *Server) verifyEVMOnce() error {
	if err := s.evmCatchUp(); err != nil {
		return err
	}
	s.evm.mu.RLock()
	gen, height, index, root, chainID := s.evm.gen, s.evm.height, s.evm.index, s.evm.root, s.evm.chain.ChainID
	s.evm.mu.RUnlock()

	assets, err := s.st.AssetsOfMimeCarried(evm.Mime)
	if err != nil {
		return err
	}
	// Only the prefix the cached chain has seen: rows indexed since belong to
	// the next catch-up, not to this comparison.
	n := len(assets)
	for n > 0 && (assets[n-1].Height > height ||
		assets[n-1].Height == height && assets[n-1].MomentumIndex > index) {
		n--
	}
	fresh := evm.NewChain(chainID)
	if err := fresh.Apply(inscriptionsOf(assets[:n])); err != nil {
		return err
	}
	got := fresh.State.Root()

	s.evm.upd.Lock()
	defer s.evm.upd.Unlock()
	if s.evm.gen != gen {
		return nil // history was rewritten while we replayed; nothing to compare
	}
	s.evm.mu.Lock()
	s.evm.verifiedAt = time.Now()
	s.evm.verifyErr = ""
	if got != root {
		s.evm.verifyErr = fmt.Sprintf("incremental root %s, full replay %s at (%d,%d)", root, got, height, index)
	}
	s.evm.mu.Unlock()
	if got != root {
		if s.log != nil {
			s.log.Error("evm: derived state disagrees with a full replay; rebuilding from genesis",
				"derived", root, "replay", got, "height", height, "index", index)
		}
		return s.evmRebuild(gen, true)
	}
	return nil
}
