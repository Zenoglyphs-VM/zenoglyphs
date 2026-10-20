// Package indexer turns momentums into Zenoglyphs state.
//
// The pipeline for every account block is:
//
//	byte-signature reject -> canonical-CBOR check -> envelope validate ->
//	opcode dispatch -> append event -> apply projection
//
// The first step exists because an indexer sees every block in every
// momentum, and full CBOR parsing to discover that a block is an ordinary
// transfer is wasted work at exactly the moment throughput matters.
package indexer

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// BlockTypeUserSend mirrors nom.BlockTypeUserSend. Only send blocks carry
// inscriptions: a receive block's data is not authored by its signer.
const BlockTypeUserSend = 2

type Config struct {
	// StartHeight is the momentum at which this protocol deployment begins.
	// Everything below it is invisible to the indexer by definition.
	StartHeight uint64
	// ConfirmationDepth is how many momentums must sit above an inscription
	// before it stops being reported as pending.
	ConfirmationDepth uint64
	// BatchSize is how many momentums to request per RPC round trip.
	BatchSize uint64
	// PollInterval is how long to wait after catching up to the tip.
	PollInterval time.Duration

	// SnapshotInterval is how many momentums pass between checkpoints of the
	// mutable projections, and SnapshotRetain is how deep a reorg those
	// checkpoints are kept to cover. See A-17.
	SnapshotInterval uint64
	SnapshotRetain   uint64
}

func DefaultConfig() Config {
	return Config{
		StartHeight: 1,
		// Full confirmation. Soft confirmation is not a depth at all — it is
		// "this indexer has parsed the block" — so there is no constant for it.
		ConfirmationDepth: 60, // ~10 minutes at a 10s momentum interval
		BatchSize:         100,
		PollInterval:      2 * time.Second,
		// One hour of reorg tolerance at NoM's ~10 s momentum interval,
		// checkpointed every ~20 minutes. A reorg shallower than
		// SnapshotRetain replays at most SnapshotInterval momentums of events
		// instead of the whole log.
		SnapshotInterval: 120,
		SnapshotRetain:   360,
	}
}

// Node is the slice of the JSON-RPC client this package uses. It is an
// interface for one reason: the reorg path is the hardest thing here to get
// right and the only thing that cannot be exercised without a node answering
// questions about heights it no longer has. *rpcclient.Client satisfies it.
type Node interface {
	FrontierMomentum(ctx context.Context) (*rpcclient.Momentum, error)
	DetailedMomentumsByHeight(ctx context.Context, height, count uint64) ([]*rpcclient.DetailedMomentum, error)
}

type Indexer struct {
	cfg    Config
	client Node
	st     *store.Store
	log    *slog.Logger

	// syncHeight is the highest momentum processed, published for /info.
	// These three are atomics because they are read from API goroutines
	// while this one writes them.
	syncHeight atomic.Uint64
	nodeHeight atomic.Uint64

	// chainID is the network the node is on, learned from the frontier
	// momentum rather than configured. It is what lets `serve` pick an
	// explorer without the operator having to state the network twice.
	chainID atomic.Uint64

	// progress counts every commit a reader could care about: each momentum
	// processed and each reorg handled. A cache derived from the projections
	// compares it with the value it last saw, and if nothing has moved it
	// has nothing to re-read (see Progress).
	progress atomic.Uint64

	// lastSnapshot is the height of the newest checkpoint, read from the
	// store at startup and maintained as checkpoints are taken.
	lastSnapshot uint64

	// processedMomentums and processingTime measure the wall-clock cost of
	// syncing so far: fetching a batch from the node and indexing it.
	// SyncRate turns this into a momentums/sec figure that /info extrapolates
	// into an approximate total sync duration. Both start at zero, which
	// reads as "no estimate yet" rather than as a stalled indexer.
	processedMomentums uint64
	processingTime     time.Duration
}

func New(cfg Config, client Node, st *store.Store, log *slog.Logger) *Indexer {
	if cfg.StartHeight == 0 {
		cfg.StartHeight = 1
	}
	if cfg.BatchSize == 0 || cfg.BatchSize > rpcclient.MaxCountSize {
		cfg.BatchSize = 100
	}
	if cfg.SnapshotRetain > 0 && cfg.SnapshotInterval == 0 {
		cfg.SnapshotInterval = 120
	}
	return &Indexer{cfg: cfg, client: client, st: st, log: log}
}

func (ix *Indexer) Config() Config     { return ix.cfg }
func (ix *Indexer) SyncHeight() uint64 { return ix.syncHeight.Load() }
func (ix *Indexer) NodeHeight() uint64 { return ix.nodeHeight.Load() }

// ChainID is the network identifier the node reports, or 0 before the first
// frontier call has answered. Callers must treat 0 as "not known yet" rather
// than as a network.
func (ix *Indexer) ChainID() uint64 { return ix.chainID.Load() }

// Progress changes after every write this indexer commits to the projections
// — each momentum, each reorg — and never otherwise. It is bumped only once
// the rows are committed, so a reader that sees a new value and then queries
// the store sees at least those rows. Its absolute value means nothing.
//
// It covers this process only. `zcribe reindex` in another process rewrites
// the same tables without touching it; store.ProjectionGeneration is what
// catches that, and a reader relying on Progress must still check the
// generation now and then.
func (ix *Indexer) Progress() uint64 { return ix.progress.Load() }

// SyncRate is the average momentums processed per second — RPC fetch plus
// indexing — measured since this process started. It is zero before the
// first batch completes; callers must read that as "no estimate yet", not
// as a rate of zero.
func (ix *Indexer) SyncRate() float64 {
	if ix.processingTime <= 0 {
		return 0
	}
	return float64(ix.processedMomentums) / ix.processingTime.Seconds()
}

// Run syncs to the chain tip and then follows it until the context ends.
func (ix *Indexer) Run(ctx context.Context) error {
	tip, err := ix.st.TipHeight()
	if err != nil {
		return err
	}
	ix.syncHeight.Store(tip)
	if snap, err := ix.st.SnapshotAtOrBelow(tip); err == nil && snap != nil {
		ix.lastSnapshot = snap.Height
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		caughtUp, err := ix.step(ctx)
		if errors.Is(err, ErrChainMismatch) {
			// Not transient and not recoverable by retrying: every further
			// poll would append another chain's blocks to this log. Stop,
			// and let the operator point at the right node or start a fresh
			// database.
			ix.log.Error("refusing to index", "err", err)
			return err
		}
		if err != nil {
			ix.log.Error("sync step failed", "err", err)
			// A transient node error must not kill the indexer; the next
			// pass re-reads from the same height and is idempotent.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
			continue
		}
		if caughtUp {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(ix.cfg.PollInterval):
			}
		}
	}
}

// step processes at most one batch. It returns true when there was nothing
// left to do.
//
// The cold-start case is the one worth stating: with an empty database the
// stored tip is 0, and the next height is the configured start height, not
// tip+1. An indexer that waits for its own height to grow before fetching
// anything spins at zero forever.
func (ix *Indexer) step(ctx context.Context) (bool, error) {
	tip, err := ix.st.TipHeight()
	if err != nil {
		return false, err
	}
	next := tip + 1
	if next < ix.cfg.StartHeight {
		next = ix.cfg.StartHeight
	}

	frontier, err := ix.client.FrontierMomentum(ctx)
	if err != nil {
		return false, err
	}
	if err := ix.checkChain(frontier); err != nil {
		return false, err
	}
	ix.nodeHeight.Store(frontier.Height)
	ix.chainID.Store(frontier.ChainIdentifier)

	// The node's tip is below ours. Reorg detection below compares a
	// momentum's parent against what we stored, and that comparison never
	// runs for a height the node no longer has — so a chain that was reset
	// out from under a running indexer looks exactly like a chain that has
	// nothing new, forever. That is how one instance kept serving 29 events
	// from a devnet that had been wiped while a fresh index of the same node
	// found 5.
	if frontier.Height < tip {
		return ix.chainWentBackwards(ctx, frontier, tip)
	}
	if next > frontier.Height {
		return true, nil
	}

	count := ix.cfg.BatchSize
	if remaining := frontier.Height - next + 1; remaining < count {
		count = remaining
	}

	fetchStart := time.Now()
	batch, err := ix.client.DetailedMomentumsByHeight(ctx, next, count)
	if err != nil {
		return false, err
	}
	if len(batch) == 0 {
		return true, nil
	}

	for _, dm := range batch {
		reorged, err := ix.processMomentum(ctx, dm)
		if err != nil {
			return false, err
		}
		if reorged {
			// The rest of this batch is from the abandoned branch. Refetch.
			return false, nil
		}
	}
	ix.processedMomentums += uint64(len(batch))
	ix.processingTime += time.Since(fetchStart)
	return false, nil
}

// ErrChainMismatch is returned when the node is on a different network than
// the one this database describes. It is fatal on purpose: continuing would
// interleave two chains' histories in one event log, and no rollback can
// separate them afterwards.
var ErrChainMismatch = errors.New("node is on a different chain than this database")

// checkChain pins the database to one network.
//
// The chain identifier is remembered the first time it is seen and compared
// on every poll after that. Pointing an existing database at another node is
// an ordinary operational mistake — a copied command line, a stale -node
// flag — and the failure it produces without this check is silent: blocks
// from the new chain append to the old chain's log, the state root chains
// over both, and the corruption is only visible as an unexplainable
// divergence from every other indexer.
//
// A reset devnet keeps its identifier, so this catches the wrong node and not
// the wiped one. chainWentBackwards is what catches that.
func (ix *Indexer) checkChain(frontier *rpcclient.Momentum) error {
	if frontier.ChainIdentifier == 0 {
		return nil // node did not say; nothing to compare
	}
	stored, err := ix.st.Meta(metaChainID)
	if err != nil {
		return err
	}
	if stored == "" {
		return ix.st.SetMeta(metaChainID, strconv.FormatUint(frontier.ChainIdentifier, 10))
	}
	if stored != strconv.FormatUint(frontier.ChainIdentifier, 10) {
		return fmt.Errorf("%w: database was built on chain %s, node reports chain %d",
			ErrChainMismatch, stored, frontier.ChainIdentifier)
	}
	return nil
}

// metaChainID is where checkChain remembers the network.
const metaChainID = "chain_id"

// chainWentBackwards handles a node whose tip is below this indexer's.
//
// Two very different things look like this, and they are told apart by asking
// what the node has at its own tip:
//
//   - The same chain, a node that is behind. A node restarted from an older
//     snapshot, or still resyncing, serves a shorter prefix of the history we
//     already have. Nothing is wrong with our index; there is simply nothing
//     to do until it catches up.
//   - A different chain of the same name. A devnet wiped and restarted, a
//     node restored from a different data directory. Every momentum we hold
//     above the fork describes blocks that no longer exist, and holding on to
//     them means serving inscriptions nobody else can see.
//
// The second is a reorg — an unusually deep one — so it is handled by the
// reorg path rather than by a second recovery mechanism.
func (ix *Indexer) chainWentBackwards(ctx context.Context, frontier *rpcclient.Momentum, tip uint64) (bool, error) {
	stored, err := ix.st.Momentum(frontier.Height)
	if err != nil {
		return false, err
	}
	if stored == nil || stored.Hash == frontier.Hash.String() {
		ix.log.Warn("node is behind this indexer; waiting for it to catch up",
			"node_height", frontier.Height, "indexed_height", tip)
		return true, nil
	}

	ix.log.Warn("the chain no longer contains the history this database holds",
		"node_height", frontier.Height, "indexed_height", tip,
		"stored_hash", stored.Hash, "chain_hash", frontier.Hash)
	if err := ix.handleReorg(ctx, frontier.Height); err != nil {
		return false, err
	}
	return false, nil
}

// processMomentum indexes one momentum. It returns true if a reorg was
// detected and handled, in which case the caller must refetch.
func (ix *Indexer) processMomentum(ctx context.Context, dm *rpcclient.DetailedMomentum) (bool, error) {
	m := dm.Momentum
	if m == nil {
		return false, fmt.Errorf("momentum missing from detailed response")
	}

	// Reorg detection: our stored hash at h-1 must be this momentum's parent.
	if m.Height > ix.cfg.StartHeight {
		prev, err := ix.st.Momentum(m.Height - 1)
		if err != nil {
			return false, err
		}
		if prev != nil && prev.Hash != m.PreviousHash.String() {
			ix.log.Warn("reorg detected",
				"height", m.Height, "stored_parent", prev.Hash, "chain_parent", m.PreviousHash)
			if err := ix.handleReorg(ctx, m.Height-1); err != nil {
				return false, err
			}
			return true, nil
		}
	}

	order, err := ix.canonicalOrder(dm)
	if err != nil {
		return false, err
	}

	for idx, block := range order {
		if err := ix.processBlock(block, m, idx); err != nil {
			return false, err
		}
	}

	root, err := ix.computeStateRoot(m.Height)
	if err != nil {
		return false, err
	}
	if err := ix.st.PutMomentum(store.MomentumRow{
		Height:       m.Height,
		Hash:         m.Hash.String(),
		PreviousHash: m.PreviousHash.String(),
		Timestamp:    m.Timestamp,
		StateRoot:    root,
	}); err != nil {
		return false, err
	}
	ix.syncHeight.Store(m.Height)
	ix.progress.Add(1)
	if err := ix.maybeSnapshot(m.Height, root); err != nil {
		// A checkpoint is an optimisation, not state. Failing to write one
		// must not stop indexing; it only means the next reorg is slower.
		ix.log.Warn("checkpoint failed", "height", m.Height, "err", err)
	}
	return false, nil
}

// maybeSnapshot checkpoints the mutable projections when enough momentums have
// passed, but only once the indexer is inside the reorg window.
//
// Backfill is deliberately excluded. A momentum a thousand deep is not going to
// be reorged, and checkpointing every interval during an initial sync of a
// million momentums would copy the balance table thousands of times to produce
// checkpoints the very next prune deletes. Crash recovery does not need them
// either: the event log survives a restart, so a restart resumes at the tip
// rather than replaying.
func (ix *Indexer) maybeSnapshot(height uint64, stateRoot string) error {
	if ix.cfg.SnapshotInterval == 0 || ix.cfg.SnapshotRetain == 0 {
		return nil
	}
	if height < ix.lastSnapshot+ix.cfg.SnapshotInterval {
		return nil
	}
	if nh := ix.nodeHeight.Load(); nh > height && nh-height > ix.cfg.SnapshotRetain {
		return nil // still backfilling
	}
	if err := ix.st.PutSnapshot(height, stateRoot); err != nil {
		return err
	}
	ix.lastSnapshot = height
	pruned, err := ix.st.PruneSnapshots(height, ix.cfg.SnapshotRetain)
	if err != nil {
		return err
	}
	ix.log.Debug("checkpoint taken", "height", height, "pruned", pruned)
	return nil
}

// canonicalOrder returns the momentum's blocks in consensus order:
// (momentum height ASC, index within momentum.content ASC).
//
// The node already returns blocks parallel to momentum.content, but the
// ordering is recomputable — NewMomentumContent canonically sorts the content
// by (address, height, hash) before hashing it, and that hash feeds the
// momentum hash. So this verifies rather than trusts: it re-derives the sort
// and, on any disagreement, uses its own answer. An indexer that trusted a
// hostile node's ordering would apply conflicting transfers in the wrong
// order, which is the whole double-spend question.
func (ix *Indexer) canonicalOrder(dm *rpcclient.DetailedMomentum) ([]*rpcclient.AccountBlock, error) {
	m := dm.Momentum
	if len(m.Content) == 0 {
		return nil, nil
	}
	byHash := make(map[types.Hash]*rpcclient.AccountBlock, len(dm.AccountBlocks))
	for _, b := range dm.AccountBlocks {
		if b != nil {
			byHash[b.Hash] = b
		}
	}

	headers := append([]rpcclient.AccountHeader(nil), m.Content...)
	expected := make([]rpcclient.AccountHeader, len(headers))
	copy(expected, headers)
	sort.SliceStable(expected, func(a, b int) bool {
		return bytes.Compare(headerBytes(expected[a]), headerBytes(expected[b])) < 0
	})
	for i := range headers {
		if headers[i].Hash != expected[i].Hash {
			ix.log.Warn("node returned momentum.content out of canonical order; using recomputed order",
				"height", m.Height, "index", i)
			headers = expected
			break
		}
	}

	out := make([]*rpcclient.AccountBlock, 0, len(headers))
	for _, h := range headers {
		b, ok := byHash[h.Hash]
		if !ok {
			// A momentum whose bodies do not match its content is unusable;
			// indexing it partially would produce state no one else has.
			return nil, fmt.Errorf("momentum %d: block %s in content but not in response",
				m.Height, h.Hash)
		}
		out = append(out, b)
	}
	return out, nil
}

// headerBytes reproduces types.AccountHeader.Bytes():
// Address ‖ BigEndian(Height) ‖ Hash. Big-endian is why lexicographic byte
// order equals numeric order, which is why a sender's own blocks always
// apply in chain order.
func headerBytes(h rpcclient.AccountHeader) []byte {
	return common.JoinBytes(h.Address.Bytes(), common.Uint64ToBytes(h.Height), h.Hash.Bytes())
}

// processBlock runs one account block through the pipeline.
func (ix *Indexer) processBlock(b *rpcclient.AccountBlock, m *rpcclient.Momentum, idx int) error {
	// Only send blocks. A receive block's data field is not authored by the
	// account that signs it, so treating it as an inscription would let
	// anyone inscribe from any address that ever received a transaction.
	if b.BlockType != BlockTypeUserSend {
		return nil
	}
	if len(b.Data) == 0 || !protocol.HasSignature(b.Data) {
		return nil
	}
	zdu, err := protocol.Decode(b.Data)
	if err != nil {
		// Not an inscription, or a malformed one. Either way it is not an
		// error for the indexer — the block simply is not ours.
		ix.log.Debug("rejected candidate block", "hash", b.Hash, "err", err)
		return nil
	}

	ev := store.Event{
		Height:        m.Height,
		MomentumIndex: idx,
		BlockHash:     b.Hash.String(),
		Opcode:        uint64(zdu.Opcode),
		Version:       zdu.Version,
		Sender:        b.Address.String(),
		Recipient:     b.ToAddress.String(),
		Payload:       zdu.Data,
		PHash:         hex.EncodeToString(zdu.PHash),
		THash:         hex.EncodeToString(zdu.THash),
		Amount:        b.Amount.String(),
		Ext:           zdu.Ext,
		TokenStandard: b.TokenStandard.String(),
		Timestamp:     m.Timestamp,

		// What the block paid for its bandwidth. FusedPlasma and Difficulty
		// are hash-committed and taken as read; BasePlasma is recomputed from
		// the data length rather than copied from b.BasePlasma, because that
		// wire field is outside the hash and go-zenon itself re-derives it
		// (vm.CanonicalBasePlasma) instead of trusting a peer's copy.
		BasePlasma:  protocol.PlasmaForDataLength(len(b.Data)),
		FusedPlasma: b.FusedPlasma,
		Difficulty:  b.Difficulty,
	}
	// A node that reports a base plasma disagreeing with the formula is
	// either running different constants or has a bug; either way the derived
	// price would be wrong on this node and right everywhere else, which is
	// exactly the kind of silent divergence worth saying out loud.
	if b.BasePlasma != 0 && b.BasePlasma != ev.BasePlasma {
		ix.log.Warn("node's basePlasma disagrees with the formula; using the formula",
			"hash", b.Hash, "node", b.BasePlasma, "computed", ev.BasePlasma)
	}

	// The event log is written before any projection, always. A projection
	// that exists without its event cannot be rebuilt, and rebuildability is
	// the property the whole design rests on.
	if err := ix.st.AppendEvent(ev); err != nil {
		return fmt.Errorf("append event %s: %w", ev.BlockHash, err)
	}
	applied, err := ix.Apply(ev)
	if err != nil {
		return fmt.Errorf("apply event %s: %w", ev.BlockHash, err)
	}
	// The outcome is recorded before the momentum's state root is computed,
	// because the root commits to exactly this set.
	if err := ix.st.SetEventApplied(ev.Height, ev.MomentumIndex, applied); err != nil {
		return fmt.Errorf("record outcome for %s: %w", ev.BlockHash, err)
	}
	return nil
}

// computeStateRoot chains a commitment over the valid inscriptions in each
// momentum:
//
//	stateRoot(h) = sha3_256( stateRoot(h-1) ‖ h ‖ orderedValidTxHashes(h) )
//
// Two indexers agreeing on stateRoot(H) agree on the entire history to H.
// When they disagree, a binary search over heights finds the exact divergent
// momentum in about twenty queries.
//
// Moderation deliberately does not participate: it is a serving-layer policy,
// so two honest operators with different policies still agree here.
func (ix *Indexer) computeStateRoot(height uint64) (string, error) {
	var prev string
	if height > ix.cfg.StartHeight {
		var err error
		prev, err = ix.st.StateRoot(height - 1)
		if err != nil {
			return "", err
		}
	}
	prevBytes, _ := hex.DecodeString(prev)
	if len(prevBytes) != protocol.HashLen {
		prevBytes = make([]byte, protocol.HashLen)
	}

	hashes, err := ix.st.TxHashesAt(height)
	if err != nil {
		return "", err
	}
	parts := [][]byte{prevBytes, common.Uint64ToBytes(height)}
	for _, h := range hashes {
		raw, err := hex.DecodeString(h)
		if err != nil {
			return "", err
		}
		parts = append(parts, raw)
	}
	return hex.EncodeToString(crypto.Hash(parts...)), nil
}

// handleReorg rewinds to the last agreeing height and replays.
//
// Replaying the whole log rather than unwinding projections incrementally is
// deliberate: an inverse for every projection write is a second
// implementation of the state machine, and the two drift. Recomputation
// cannot drift.
func (ix *Indexer) handleReorg(ctx context.Context, from uint64) error {
	agreed, err := ix.findLastAgreeingHeight(ctx, from)
	if err != nil {
		return err
	}
	ix.log.Warn("rolling back", "to_height", agreed)

	if err := ix.st.TruncateAbove(agreed); err != nil {
		return err
	}
	// Checkpoints above the fork describe a branch that no longer exists.
	// Restoring from one would silently reinstate the abandoned history.
	if err := ix.st.DropSnapshotsAbove(agreed); err != nil {
		return err
	}
	if err := ix.RebuildFrom(agreed); err != nil {
		return err
	}
	ix.syncHeight.Store(agreed)
	ix.progress.Add(1)
	ix.lastSnapshot = 0
	if snap, err := ix.st.SnapshotAtOrBelow(agreed); err == nil && snap != nil {
		ix.lastSnapshot = snap.Height
	}
	return nil
}

// findLastAgreeingHeight returns the highest height at or below `from` where
// this indexer's stored momentum hash matches the chain's.
//
// Two phases, because the depth is not known in advance and the two extremes
// have opposite costs. An ordinary fork is a handful of momentums deep, and a
// linear walk finds it in a handful of round trips. A chain that was reset
// disagrees everywhere, and the same linear walk would ask the node about
// every momentum ever indexed — seventeen thousand round trips to discover
// there is nothing to keep.
//
// So: step back in doubling strides until a height agrees (or the search
// falls off the bottom), then binary search the interval between the last
// disagreement and the first agreement. Shallow forks still cost one or two
// probes, and a full reset costs about 2·log2(depth).
func (ix *Indexer) findLastAgreeingHeight(ctx context.Context, from uint64) (uint64, error) {
	floor := ix.cfg.StartHeight - 1 // "nothing to keep"

	// Both phases assume the chain agrees on a prefix: if a height matches,
	// everything below it does too. That is what a fork means, and it is the
	// same assumption the linear walk made by stopping at its first match.
	//
	// Phase one: find an agreeing height, and keep the lowest height known to
	// disagree above it.
	lo, hi := floor, from // lo agrees (vacuously), hi is unknown
	lowestBad := from + 1
	for step := uint64(1); ; step *= 2 {
		agrees, err := ix.agreesAt(ctx, hi)
		if err != nil {
			return 0, err
		}
		if agrees {
			lo = hi
			break
		}
		lowestBad = hi
		if hi <= floor+step {
			// Everything down to the floor disagrees; nothing survives.
			return floor, nil
		}
		hi -= step
	}
	if lo == from {
		return from, nil
	}

	// Phase two: the fork is in (lo, lowestBad). Everything at or below lo
	// agrees and everything at or above lowestBad does not, so narrow until
	// they meet.
	high := lowestBad
	for high-lo > 1 {
		mid := lo + (high-lo)/2
		agrees, err := ix.agreesAt(ctx, mid)
		if err != nil {
			return 0, err
		}
		if agrees {
			lo = mid
		} else {
			high = mid
		}
	}
	return lo, nil
}

// agreesAt reports whether the chain still contains the momentum this indexer
// stored at a height. A height we never stored counts as agreeing: there is
// nothing there to be wrong, and the search is looking for the highest point
// with nothing above it to unwind.
func (ix *Indexer) agreesAt(ctx context.Context, h uint64) (bool, error) {
	if h < ix.cfg.StartHeight {
		return true, nil
	}
	stored, err := ix.st.Momentum(h)
	if err != nil {
		return false, err
	}
	if stored == nil {
		return true, nil
	}
	live, err := ix.client.DetailedMomentumsByHeight(ctx, h, 1)
	if err != nil {
		return false, err
	}
	return len(live) > 0 && live[0].Momentum != nil &&
		live[0].Momentum.Hash.String() == stored.Hash, nil
}

// RebuildFrom recomputes state as of a height, starting from the newest
// checkpoint at or below it.
//
// This is the same replay as Rebuild with a later base case, not a second way
// of computing state: Apply is untouched and only the starting point moves.
// With no usable checkpoint it degrades to exactly Rebuild, which is why a
// missing or corrupt checkpoint costs time and never correctness.
func (ix *Indexer) RebuildFrom(height uint64) error {
	if ix.cfg.SnapshotRetain == 0 {
		return ix.Rebuild()
	}
	snap, err := ix.st.SnapshotAtOrBelow(height)
	if err != nil {
		return err
	}
	if snap == nil {
		ix.log.Info("no checkpoint at or below the rollback point; replaying from the start",
			"height", height)
		return ix.Rebuild()
	}
	if err := ix.st.RestoreSnapshot(snap.Height); err != nil {
		ix.log.Warn("checkpoint restore failed; replaying from the start",
			"height", snap.Height, "err", err)
		return ix.Rebuild()
	}

	var n, applied int
	if err := ix.st.EventsAbove(snap.Height, func(e store.Event) error {
		n++
		ok, err := ix.Apply(e)
		if err != nil {
			return err
		}
		if ok {
			applied++
		}
		return ix.st.SetEventApplied(e.Height, e.MomentumIndex, ok)
	}); err != nil {
		return err
	}
	// Roots at or below the checkpoint cannot have moved: their applied flags
	// were never touched, and the chain is determined by its prefix.
	roots, err := ix.recomputeStateRootsAbove(snap.Height)
	if err != nil {
		return err
	}
	ix.log.Info("rebuilt from checkpoint", "checkpoint", snap.Height,
		"events_replayed", n, "applied", applied, "roots_recomputed", roots)
	return nil
}

// Rebuild drops every projection and replays the event log in canonical
// order. It is the single supported way to recompute state from nothing, used
// by both reorg recovery and the reindex command — sharing one path is what
// keeps the two from diverging.
func (ix *Indexer) Rebuild() error {
	if err := ix.st.ResetProjections(); err != nil {
		return err
	}
	var n, applied int
	err := ix.st.Events(func(e store.Event) error {
		n++
		ok, err := ix.Apply(e)
		if err != nil {
			return err
		}
		if ok {
			applied++
		}
		// The applied flag is derived state, so it is recomputed here rather
		// than trusted from the previous run. A rules change between runs
		// must show up as a different flag — and then as a different root.
		return ix.st.SetEventApplied(e.Height, e.MomentumIndex, ok)
	})
	if err != nil {
		return err
	}
	// Roots chain off the applied set, so they have to be recomputed too.
	// Skipping this is how a reindex silently leaves a stale commitment
	// describing state that no longer exists.
	roots, err := ix.recomputeStateRootsAbove(0)
	if err != nil {
		return err
	}
	ix.log.Info("rebuilt from event log",
		"events", n, "applied", applied, "roots_recomputed", roots)
	return nil
}

// recomputeStateRootsAbove rewrites the commitment chain above a height, in
// height order so each root sees its predecessor's new value. Passing 0
// rewrites all of it.
func (ix *Indexer) recomputeStateRootsAbove(from uint64) (int, error) {
	heights, err := ix.st.MomentumHeights()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, h := range heights {
		if h <= from {
			continue
		}
		n++
		root, err := ix.computeStateRoot(h)
		if err != nil {
			return 0, err
		}
		if err := ix.st.SetStateRoot(h, root); err != nil {
			return 0, err
		}
	}
	return n, nil
}
