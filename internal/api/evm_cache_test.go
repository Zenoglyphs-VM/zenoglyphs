package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/evm"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// The counter contract from internal/evm/chain_test.go: increment() is
// d09de08a, and slot 0 holds the count.
const cacheCounterInit = "6100858061000d6000396000f360003560e01c8063d09de08a1461002c5780636d4ce63c146100655780631003e2d2146100715760006000fd5b600054600101806000556000527f20d8a6f5a693f9d1d627a598e8820f7a55ee74c183aa8f1a30e8d4e8dd9a8d8460206000a160206000f35b60005460005260206000f35b600435600054018060005560005260206000f3"

type evmFixture struct {
	t       *testing.T
	st      *store.Store
	number  int64
	counter evm.Address
}

func (f *evmFixture) id(n byte) string {
	b := make([]byte, 32)
	b[0], b[31] = 0xee, n
	return hex.EncodeToString(b)
}

func (f *evmFixture) put(n byte, height uint64, content []byte, ext map[string]any) string {
	f.t.Helper()
	raw, err := protocol.Marshal(map[string]any{"evm": ext})
	if err != nil {
		f.t.Fatal(err)
	}
	id := f.id(n)
	if err := f.st.PutAsset(store.Asset{
		ID: id, Number: f.number, Issuer: "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d", Owner: "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d",
		Supply: 1, Mime: evm.Mime, Content: content, Ext: raw, PartCount: 1,
		Height: height, Timestamp: 1700000000 + int64(height),
	}); err != nil {
		f.t.Fatal(err)
	}
	f.number++
	return id
}

func (f *evmFixture) deploy(n byte, height uint64) {
	f.t.Helper()
	code, _ := hex.DecodeString(cacheCounterInit)
	id := f.put(n, height, code, map[string]any{"op": "deploy"})
	b, _ := hex.DecodeString(id)
	f.counter = evm.ContractAddress(b)
}

func (f *evmFixture) increment(n byte, height uint64) string {
	return f.put(n, height, []byte{0xd0, 0x9d, 0xe0, 0x8a}, map[string]any{"op": "call", "to": f.counter.Hex()})
}

func overview(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rec := do(t, s.Routes(nil), http.MethodGet, "/evm/overview", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func counterValue(t *testing.T, s *Server, f *evmFixture) string {
	t.Helper()
	s.evm.mu.RLock()
	defer s.evm.mu.RUnlock()
	return s.evm.chain.State.Load(f.counter, new(big.Int)).String()
}

// New inscriptions are applied onto the cached chain, not by replaying it.
func TestEVMCacheFollowsTheTail(t *testing.T) {
	s, st := newServer(t)
	f := &evmFixture{t: t, st: st}
	f.deploy(1, 100)
	f.increment(2, 101)

	if o := overview(t, s); o["txs"] != 2.0 || o["rebuilds"] != 1.0 {
		t.Fatalf("after the first read: txs=%v rebuilds=%v, want 2 and 1", o["txs"], o["rebuilds"])
	}
	last := ""
	for n := byte(3); n <= 6; n++ {
		last = f.increment(n, 100+uint64(n))
	}
	o := overview(t, s)
	if o["txs"] != 6.0 {
		t.Fatalf("txs = %v after four more increments, want 6", o["txs"])
	}
	if o["rebuilds"] != 1.0 {
		t.Fatalf("rebuilds = %v: the tail was replayed from scratch instead of applied", o["rebuilds"])
	}
	if got := counterValue(t, s, f); got != "5" {
		t.Fatalf("counter = %s, want 5", got)
	}
	if rec := do(t, s.Routes(nil), http.MethodGet, "/evm/tx/"+last, nil); rec.Code != http.StatusOK {
		t.Fatalf("/evm/tx for the newest call: %d", rec.Code)
	}

	// The incremental chain must be the chain a full replay produces.
	if err := s.verifyEVMOnce(); err != nil {
		t.Fatal(err)
	}
	if s.evm.verifyErr != "" {
		t.Fatalf("verification disagreed: %s", s.evm.verifyErr)
	}
}

// A rewrite that keeps the number of EVM inscriptions the same — a reorg that
// swaps one call for another — must still be noticed. Keying the cache on the
// count, as it was until 2026-09-11, served the abandoned branch.
func TestEVMCacheRebuildsWhenHistoryIsRewritten(t *testing.T) {
	s, st := newServer(t)
	f := &evmFixture{t: t, st: st}
	f.deploy(1, 100)
	f.increment(2, 101)
	f.increment(3, 102)
	if got := overview(t, s)["txs"]; got != 3.0 {
		t.Fatalf("txs = %v, want 3", got)
	}
	before := overview(t, s)["stateRoot"]

	// The rewrite: the projections are reset and the branch that replaces the
	// old one has the same deploy and the same number of calls, one of which
	// is now a malformed selector that reverts.
	if err := st.ResetProjections(); err != nil {
		t.Fatal(err)
	}
	f.number = 0
	f.deploy(1, 100)
	f.increment(2, 101)
	f.put(9, 102, []byte{0xde, 0xad, 0xbe, 0xef}, map[string]any{"op": "call", "to": f.counter.Hex()})

	o := overview(t, s)
	if o["txs"] != 3.0 {
		t.Fatalf("txs = %v, want 3", o["txs"])
	}
	if o["rebuilds"] != 2.0 {
		t.Fatalf("rebuilds = %v: a same-count rewrite went unnoticed", o["rebuilds"])
	}
	if o["stateRoot"] == before {
		t.Fatal("state root unchanged across a rewrite that changed the counter")
	}
	if got := counterValue(t, s, f); got != "1" {
		t.Fatalf("counter = %s on the new branch, want 1", got)
	}
}

// Looking an address up must not add it to the state being shown. The
// interpreter's reads create accounts, and until 2026-09-11 the API used them,
// so every lookup of an unknown address changed the served state root.
func TestEVMReadsDoNotChangeState(t *testing.T) {
	s, st := newServer(t)
	f := &evmFixture{t: t, st: st}
	f.deploy(1, 100)
	f.increment(2, 101)

	before := overview(t, s)
	h := s.Routes(nil)
	for i := 0; i < 5; i++ {
		addr := fmt.Sprintf("0x00000000000000000000000000000000000000%02x", 0xa0+i)
		if rec := do(t, h, http.MethodGet, "/evm/account/"+addr, nil); rec.Code != http.StatusOK {
			t.Fatalf("/evm/account: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := do(t, h, http.MethodGet, "/evm/token/"+f.counter.Hex(), nil); rec.Code != http.StatusOK {
		t.Fatalf("/evm/token: %d", rec.Code)
	}
	after := overview(t, s)
	if after["accounts"] != before["accounts"] || after["stateRoot"] != before["stateRoot"] {
		t.Fatalf("reads changed the state: accounts %v→%v, root %v→%v",
			before["accounts"], after["accounts"], before["stateRoot"], after["stateRoot"])
	}
	s.evm.mu.RLock()
	defer s.evm.mu.RUnlock()
	if got := s.evm.chain.State.Root(); got != s.evm.root {
		t.Fatalf("live root %s differs from the root computed at apply time %s", got, s.evm.root)
	}
}

// A divergence between the incremental chain and a full replay is reported
// and repaired by a rebuild.
func TestEVMVerificationRepairsDivergence(t *testing.T) {
	s, st := newServer(t)
	f := &evmFixture{t: t, st: st}
	f.deploy(1, 100)
	f.increment(2, 101)
	overview(t, s)

	// Corrupt the cached chain the way a bookkeeping bug would.
	s.evm.mu.Lock()
	s.evm.chain.State.Store(f.counter, new(big.Int), big.NewInt(1000))
	s.evm.chain.State.Settle()
	s.evm.root = s.evm.chain.State.Root()
	s.evm.mu.Unlock()

	if err := s.verifyEVMOnce(); err != nil {
		t.Fatal(err)
	}
	if s.evm.verifyErr == "" {
		t.Fatal("verification did not notice a corrupted chain")
	}
	if s.evm.rebuilds != 2 {
		t.Fatalf("rebuilds = %d, want the divergence repaired by a rebuild", s.evm.rebuilds)
	}
	if got := counterValue(t, s, f); got != "1" {
		t.Fatalf("counter = %s after the repair, want 1", got)
	}
}

// Readers and the tail run concurrently. Under -race this is the test that
// catches a handler writing to shared state or reading a half-applied tail —
// with the store checked on every request, and with the lock-free skip in
// play, which is what production runs.
func TestEVMCacheConcurrentReadersAndTail(t *testing.T) {
	for _, stale := range []time.Duration{0, 5 * time.Millisecond} {
		t.Run(stale.String(), func(t *testing.T) { concurrentReadersAndTail(t, stale) })
	}
}

func concurrentReadersAndTail(t *testing.T, stale time.Duration) {
	s, st := newServer(t)
	s.EVMMaxStale = stale
	f := &evmFixture{t: t, st: st}
	f.deploy(1, 100)
	overview(t, s)
	h := s.Routes(nil)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, p := range []string{"/evm/overview", "/evm/token/" + f.counter.Hex(),
					"/evm/account/" + f.counter.Hex(), "/evm/txs"} {
					if rec := do(t, h, http.MethodGet, p, nil); rec.Code != http.StatusOK {
						t.Errorf("%s: %d", p, rec.Code)
						return
					}
				}
			}
		}()
	}
	for n := byte(2); n <= 60; n++ {
		f.increment(n, 100+uint64(n))
	}
	// Let the readers pick up the last of the tail before stopping them.
	for overview(t, s)["txs"] != 60.0 {
	}
	close(stop)
	wg.Wait()
	if got := counterValue(t, s, f); got != "59" {
		t.Fatalf("counter = %s, want 59", got)
	}
}
