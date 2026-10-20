package api

import (
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/evm"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/indexer"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// checkpointServer is a server that knows its chain — checkpoints are per
// chain, and one that has never heard of its network takes none — and
// checkpoints every `every` inscriptions.
func checkpointServer(t *testing.T, st *store.Store, ix *indexer.Indexer, every int) *Server {
	t.Helper()
	if err := st.SetMeta("chain_id", "69"); err != nil {
		t.Fatal(err)
	}
	s := New(st, ix, "http://content.test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.EVMMaxStale = 0
	s.EVMCheckpointEvery = every
	return s
}

// A restart resumes from the newest checkpoint instead of replaying from
// genesis, and lands on exactly the state the first process had.
func TestEVMRestartResumesFromCheckpoint(t *testing.T) {
	first, st := newServer(t)
	first = checkpointServer(t, st, first.ix, 4)
	f := &evmFixture{t: t, st: st}
	f.deploy(1, 100)
	for n := byte(2); n <= 10; n++ {
		f.increment(n, 100+uint64(n))
		overview(t, first) // catch up one row at a time, crossing checkpoints
	}
	want := overview(t, first)
	if want["checkpointAt"] == "" {
		t.Fatal("no checkpoint was written")
	}

	second := checkpointServer(t, st, first.ix, 4)
	got := overview(t, second)
	if from, _ := got["restoredFrom"].(string); !strings.HasPrefix(from, "checkpoint") {
		t.Fatalf("restart restored from %q, want a checkpoint", from)
	}
	for _, k := range []string{"stateRoot", "txs", "accounts", "failed", "applied"} {
		if got[k] != want[k] {
			t.Fatalf("%s after the restart = %v, want %v", k, got[k], want[k])
		}
	}
	// Receipts from before the checkpoint came back with it.
	if rec := do(t, second.Routes(nil), "GET", "/evm/tx/"+f.id(2), nil); rec.Code != 200 {
		t.Fatalf("/evm/tx for a receipt inside the checkpoint: %d", rec.Code)
	}
	if err := second.verifyEVMOnce(); err != nil || second.evm.verifyErr != "" {
		t.Fatalf("resumed state disagrees with a full replay: %v %s", err, second.evm.verifyErr)
	}
}

// A checkpoint whose bytes do not restore to the root it was written with is
// refused, and the restart replays from genesis instead of trusting it.
func TestEVMCorruptCheckpointFallsBackToGenesis(t *testing.T) {
	first, st := newServer(t)
	first = checkpointServer(t, st, first.ix, 2)
	f := &evmFixture{t: t, st: st}
	f.deploy(1, 100)
	for n := byte(2); n <= 6; n++ {
		f.increment(n, 100+uint64(n))
		overview(t, first)
	}
	want := overview(t, first)["stateRoot"]
	if _, err := st.DB().Exec(`UPDATE evm_checkpoints SET root = 'forged'`); err != nil {
		t.Fatal(err)
	}

	second := checkpointServer(t, st, first.ix, 2)
	got := overview(t, second)
	if got["restoredFrom"] != "genesis" {
		t.Fatalf("restored from %v; a forged checkpoint was trusted", got["restoredFrom"])
	}
	if got["stateRoot"] != want {
		t.Fatalf("root %v after the fallback, want %v", got["stateRoot"], want)
	}
}

// The fast path skips the store while the indexer has committed nothing, but
// never for longer than EVMMaxStale — which is what catches rows written by
// another process, as `zcribe reindex` does.
func TestEVMSkipIsBoundedByMaxStale(t *testing.T) {
	s, st := newServer(t)
	s.EVMMaxStale = 300 * time.Millisecond
	f := &evmFixture{t: t, st: st}
	f.deploy(1, 100)
	f.increment(2, 101)
	if got := overview(t, s)["txs"]; got != 2.0 {
		t.Fatalf("txs = %v, want 2", got)
	}

	// Written behind the indexer's back: Progress does not move.
	f.increment(3, 103)
	if got := overview(t, s)["txs"]; got != 2.0 {
		t.Fatalf("txs = %v straight away; the store was read although nothing announced a change", got)
	}
	time.Sleep(350 * time.Millisecond)
	if got := overview(t, s)["txs"]; got != 3.0 {
		t.Fatalf("txs = %v after EVMMaxStale; the store was never read again", got)
	}
}

// evEvent builds indexer events carrying EVM inscriptions, one per momentum.
type evEvent struct {
	t      *testing.T
	st     *store.Store
	ix     *indexer.Indexer
	height uint64
}

func (b *evEvent) mint(content []byte, ext map[string]any) string {
	b.t.Helper()
	b.height++
	raw, err := protocol.Marshal(map[string]any{"evm": ext})
	if err != nil {
		b.t.Fatal(err)
	}
	payload, err := protocol.Marshal(&protocol.MintWire{Name: "call.evm", Mime: evm.Mime, Supply: 1, Data: content, Ext: raw})
	if err != nil {
		b.t.Fatal(err)
	}
	hash := hex.EncodeToString(types.NewHash([]byte(fmt.Sprintf("%d-%x", b.height, content))).Bytes())
	e := store.Event{
		Height: b.height, MomentumIndex: 0, BlockHash: hash,
		Opcode: uint64(protocol.OpMint), Version: protocol.Version,
		Sender: "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d", Recipient: "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d",
		Payload: payload, Amount: "0", TokenStandard: types.ZnnTokenStandard.String(),
		Timestamp: int64(1700000000 + b.height),
	}
	if err := b.st.AppendEvent(e); err != nil {
		b.t.Fatal(err)
	}
	applied, err := b.ix.Apply(e)
	if err != nil {
		b.t.Fatal(err)
	}
	if err := b.st.SetEventApplied(e.Height, e.MomentumIndex, applied); err != nil {
		b.t.Fatal(err)
	}
	if err := b.st.PutMomentum(store.MomentumRow{Height: b.height, Hash: "h", PreviousHash: "p", Timestamp: e.Timestamp}); err != nil {
		b.t.Fatal(err)
	}
	return hash
}

// The end-to-end reorg: real events through the indexer, checkpoints on both
// sides of the fork, and the indexer's own rollback. The derived EVM must
// resume from a checkpoint below the fork — the ones above it describe the
// abandoned branch and must be gone — and land on the new branch's state.
func TestEVMReorgThroughTheIndexer(t *testing.T) {
	s, st := newServer(t)
	s = checkpointServer(t, st, s.ix, 3)
	b := &evEvent{t: t, st: st, ix: s.ix}

	code, _ := hex.DecodeString(cacheCounterInit)
	deploy := b.mint(code, map[string]any{"op": "deploy"})
	did, _ := hex.DecodeString(deploy)
	counter := evm.ContractAddress(did)
	inc := func() string {
		return b.mint([]byte{0xd0, 0x9d, 0xe0, 0x8a}, map[string]any{"op": "call", "to": counter.Hex()})
	}
	for b.height < 10 {
		inc()
		overview(t, s)
	}
	if err := st.PutSnapshot(10, ""); err != nil { // the indexer's own checkpoint
		t.Fatal(err)
	}
	for b.height < 20 {
		inc()
		overview(t, s)
	}
	if got := counterAt(t, s, counter); got != "19" {
		t.Fatalf("counter = %s before the fork, want 19", got)
	}

	// The chain agrees to 12 and rewrote everything above: roll back the way
	// handleReorg does, then index the new branch — two calls that revert.
	const agreed = 12
	if err := st.TruncateAbove(agreed); err != nil {
		t.Fatal(err)
	}
	if err := st.DropSnapshotsAbove(agreed); err != nil {
		t.Fatal(err)
	}
	if err := s.ix.RebuildFrom(agreed); err != nil {
		t.Fatal(err)
	}
	b.height = agreed
	b.mint([]byte{0xde, 0xad, 0xbe, 0xef}, map[string]any{"op": "call", "to": counter.Hex()})
	b.mint([]byte{0xde, 0xad, 0xbe, 0xef}, map[string]any{"op": "call", "to": counter.Hex()})

	o := overview(t, s)
	from, _ := o["restoredFrom"].(string)
	if !strings.HasPrefix(from, "checkpoint") {
		t.Fatalf("after the reorg restored from %q, want a checkpoint below the fork", from)
	}
	var h, i int
	if _, err := fmt.Sscanf(from, "checkpoint %d:%d", &h, &i); err != nil || h > 10 {
		t.Fatalf("restored from %q: a checkpoint above the fork survived the rollback", from)
	}
	if got := counterAt(t, s, counter); got != "11" {
		t.Fatalf("counter = %s on the new branch, want 11 (increments to 12, then two reverts)", got)
	}
	if o["txs"] != 14.0 || o["failed"] != 2.0 {
		t.Fatalf("txs=%v failed=%v on the new branch, want 14 and 2", o["txs"], o["failed"])
	}
	if err := s.verifyEVMOnce(); err != nil || s.evm.verifyErr != "" {
		t.Fatalf("post-reorg state disagrees with a full replay: %v %s", err, s.evm.verifyErr)
	}
}

func counterAt(t *testing.T, s *Server, addr evm.Address) string {
	t.Helper()
	s.evm.mu.RLock()
	defer s.evm.mu.RUnlock()
	return s.evm.chain.State.Load(addr, new(big.Int)).String()
}
