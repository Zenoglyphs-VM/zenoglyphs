package main

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/evm"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/inscribe"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
)

// zcribe evm simulate generates real EVM activity — a deploy, then a mix of
// contract calls — so the explorer, the indexer, and this file's own state
// arithmetic all have something to disagree about if one of them is wrong.
//
// It is not a fuzzer and not a load test. It is a small, honest client: every
// call it makes is a real inscription, confirmed on NoM and then read back
// through the same public API a browser would use, and every outcome is
// checked against what the script itself expects to have happened. That
// second part is the reason it exists — a script that only fires transactions
// and never looks at the result would not have caught the ambiguous-column
// bug that prompted writing this, because that bug did not stop inscriptions
// from being submitted, only from being read back correctly.
//
// simCounterInit and simERC20Init are the same hand-assembled fixtures the
// evm package's own tests replay (see internal/evm/chain_test.go and
// erc20_test.go for the opcode-level commentary). They are copied here rather
// than exported from the test files because they are fixtures, not library
// code — duplicating two bytecode strings is simpler than turning test
// fixtures into something other packages import.
const (
	simCounterInit = "6100858061000d6000396000f360003560e01c8063d09de08a1461002c5780636d4ce63c146100655780631003e2d2146100715760006000fd5b600054600101806000556000527f20d8a6f5a693f9d1d627a598e8820f7a55ee74c183aa8f1a30e8d4e8dd9a8d8460206000a160206000f35b60005460005260206000f35b600435600054018060005560005260206000f3"
	simERC20Init   = "7f00000000000000000000000000000000000000000000d3c21bcecceda1000000806000553360005260016020526040600020556102ae806100416000396000f360003560e01c806318160ddd1461005857806370a082311461006f578063a9059cbb1461009f578063095ea7b314610123578063dd62ed3e1461019357806323b872dd146101e7578063313ce567146100645760006000fd5b60005460005260206000f35b601260005260206000f35b60043573ffffffffffffffffffffffffffffffffffffffff16600052600160205260406000205460005260206000f35b60243560043573ffffffffffffffffffffffffffffffffffffffff16336000526001602052604060002080548311610053578054839003905580600052600160205260406000208054830190558160005280337fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a3600160005260206000f35b60243560043573ffffffffffffffffffffffffffffffffffffffff1633600052600260205260406000206020528060005260406000208290558160005280337f8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b92560206000a3600160005260206000f35b60043573ffffffffffffffffffffffffffffffffffffffff166000526002602052604060002060205260243573ffffffffffffffffffffffffffffffffffffffff1660005260406000205460005260206000f35b60443560243573ffffffffffffffffffffffffffffffffffffffff1660043573ffffffffffffffffffffffffffffffffffffffff16806000526002602052604060002060205233600052604060002080548411610053578054849003905580600052600160205260406000208054841161005357805484900390559080600052600160205260406000208054840190558260005280827fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef60206000a3600160005260206000f3"
)

func evmSimulate(ctx context.Context, args []string) error {
	set := fs("evm simulate")
	var w walletFlags
	w.register(set)
	var sf simFlags
	sf.register(set)
	rounds := set.Int("rounds", 20, "random operations to run after deploying")
	_ = set.Parse(args)

	runID := hex.EncodeToString(randBytes(4))
	log := newLogger(w.verbose).With("run", runID, "component", "evm-sim")
	log.Info("starting EVM activity simulation", "rounds", *rounds, "seed", *sf.seed, "api", *sf.api, "fail_rate", *sf.failRate)

	in, err := w.inscriber(ctx)
	if err != nil {
		log.Error("cannot build wallet", "err", err)
		return fmt.Errorf("wallet: %w", err)
	}
	log.Info("wallet ready", "address", in.Address())

	sim, err := sf.newSim(ctx, in, log)
	if err != nil {
		return err
	}

	if err := sim.setup(*sf.counter, *sf.token); err != nil {
		log.Error("setup failed", "err", err)
		return err
	}

	for i := 1; i <= *rounds; i++ {
		sim.round(i)
	}

	sim.summarize()
	if sim.failures > 0 || sim.mismatches > 0 {
		return fmt.Errorf("simulation finished with %d unexpected failure(s) and %d state mismatch(es) — see run=%s above for exactly which round and inscription",
			sim.failures, sim.mismatches, runID)
	}
	return nil
}

// simFlags are the knobs evm simulate and evm swarm share: where state is
// read back from, which contracts to use, how often to misbehave on purpose,
// and how much gas to pay for.
type simFlags struct {
	api       *string
	counter   *string
	token     *string
	failRate  *float64
	indexWait *time.Duration
	seed      *int64
	deployGas *uint64
	callGas   *uint64
	feeToken  *string
	headroom  *uint64
	rebid     *time.Duration
}

func (f *simFlags) register(set *flag.FlagSet) {
	f.api = set.String("api", "http://127.0.0.1:8710", "zcribe API base URL — activity is confirmed by reading it back through here")
	f.counter = set.String("counter", "", "reuse an existing counter contract instead of deploying one")
	f.token = set.String("token", "", "reuse an existing ERC-20 instead of deploying one")
	f.failRate = set.Float64("fail-rate", 0.15, "fraction of rounds that deliberately send a malformed call, to exercise the failure path")
	f.indexWait = set.Duration("index-wait", 90*time.Second, "how long to wait for the indexer to catch up to each inscription")
	f.seed = set.Int64("seed", time.Now().UnixNano(), "random seed, for a reproducible run")
	f.deployGas = set.Uint64("deploy-gas", defaultDeployGas, "gas each deploy may use, and what its burned fee is quoted for")
	f.callGas = set.Uint64("call-gas", defaultCallGas, "gas each call may use, and what its burned fee is quoted for")
	f.feeToken = set.String("fee-token", "znn", "burn znn or qsr to pay for gas")
	// 25% outlasts two momentums of the steepest climb measured (+10% each,
	// 1.1^2 = 1.21), and costs 25% more fee per call, because the deriver
	// prices the burn at what the block paid.
	f.headroom = set.Uint64("price-headroom", 25, "percent of fused plasma paid over the network price, so a block survives a momentum or two of rising price")
	f.rebid = set.Duration("rebid-after", 30*time.Second, "re-sign a call that has not confirmed after this long at the current price, replacing it; 0 never rebids")
}

// newSim builds a simulator around one publishing wallet, which deploys the
// contracts and owns the ERC-20's initial supply.
func (f *simFlags) newSim(ctx context.Context, in *inscribe.Inscriber, log *slog.Logger) (*evmSim, error) {
	deployer, err := evm.AddressOf(in.Address().String())
	if err != nil {
		return nil, fmt.Errorf("map wallet address into the EVM: %w", err)
	}
	in.HeadroomPercent = *f.headroom
	sim := &evmSim{
		ctx: ctx, in: in, api: strings.TrimRight(*f.api, "/"),
		rebidAfter: *f.rebid,
		httpc:      &http.Client{Timeout: 30 * time.Second},
		log:        log, rng: rand.New(rand.NewSource(*f.seed)),
		indexWait: *f.indexWait, failRate: *f.failRate,
		deployGas: *f.deployGas, callGas: *f.callGas, feeToken: *f.feeToken,
		deployer:         deployer,
		expectedBalances: map[evm.Address]*big.Int{},
	}
	// A handful of fixed synthetic recipients — arbitrary 20-byte values, not
	// real wallets — so transfers have somewhere to go and the token page
	// shows more than one holder.
	for i := byte(1); i <= 4; i++ {
		var a evm.Address
		a[19] = i
		sim.recipients = append(sim.recipients, a)
	}
	return sim, nil
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = crand.Read(b)
	return b
}

type evmSim struct {
	ctx   context.Context
	in    *inscribe.Inscriber
	api   string
	httpc *http.Client
	log   *slog.Logger
	rng   *rand.Rand

	indexWait  time.Duration
	failRate   float64
	rebidAfter time.Duration

	deployGas uint64
	callGas   uint64
	feeToken  string

	counter  evm.Address
	token    evm.Address
	deployer evm.Address

	recipients []evm.Address

	// expectedCounter and expectedBalances are this run's own arithmetic,
	// updated the moment a call is confirmed to have succeeded. Every
	// verify* call compares this against what the public API reports, which
	// is the whole point: two independent computations of the same state
	// that must agree.
	expectedCounter  *big.Int
	expectedBalances map[evm.Address]*big.Int

	ops        int
	failures   int
	mismatches int

	// rec, when set, gets one row per call submitted (see evmbench.go);
	// wallet labels the rows. lastFee is what the latest publish burned, left
	// for submitCall to record — each swarm wallet has its own evmSim, so no
	// other goroutine writes it.
	rec     *benchRecorder
	wallet  int
	lastFee *big.Int
}

func (s *evmSim) setup(counterAddr, tokenAddr string) error {
	s.expectedCounter = new(big.Int)

	if counterAddr != "" {
		addr, err := evm.ParseAddress(counterAddr)
		if err != nil {
			return fmt.Errorf("-counter: %w", err)
		}
		s.counter = addr
		// A reused counter carries every earlier run's increments, so the
		// baseline is whatever it holds now, not zero.
		start, err := s.readCounter()
		if err != nil {
			return fmt.Errorf("read starting counter value: %w", err)
		}
		s.expectedCounter = start
		s.log.Info("reusing counter contract", "address", addr.Hex(), "value", start.String())
	} else {
		addr, err := s.deploy("counter", simCounterInit)
		if err != nil {
			return fmt.Errorf("deploy counter: %w", err)
		}
		s.counter = addr
	}

	if tokenAddr != "" {
		addr, err := evm.ParseAddress(tokenAddr)
		if err != nil {
			return fmt.Errorf("-token: %w", err)
		}
		s.token = addr
		s.log.Info("reusing ERC-20 contract", "address", addr.Hex())
		data, err := s.get("/evm/token/" + s.token.Hex())
		if err != nil {
			return fmt.Errorf("read starting token state: %w", err)
		}
		bal := new(big.Int)
		for _, raw := range asSlice(data["holders"]) {
			m, _ := raw.(map[string]any)
			if strings.EqualFold(str(m["address"]), s.deployer.Hex()) {
				bal.SetString(str(m["balance"]), 10)
			}
		}
		s.expectedBalances[s.deployer] = bal
	} else {
		addr, err := s.deploy("erc20", simERC20Init)
		if err != nil {
			return fmt.Errorf("deploy token: %w", err)
		}
		s.token = addr
		oneMillion, _ := new(big.Int).SetString("1000000000000000000000000", 10)
		s.expectedBalances[s.deployer] = oneMillion
	}
	return nil
}

// deploy publishes init code as a new contract, waits for it to be confirmed
// on NoM and then indexed into EVM state, and returns its address. Every
// stage logs the inscription id, so a deploy that never shows up on the
// overview page can be traced to exactly which stage it stalled or failed at.
func (s *evmSim) deploy(name, initHex string) (evm.Address, error) {
	code, err := hex.DecodeString(initHex)
	if err != nil {
		return evm.Address{}, fmt.Errorf("decode %s fixture: %w", name, err)
	}
	rl := s.log.With("op", "deploy", "contract", name)

	start := time.Now()
	hash, err := s.publish(rl, name+".evm", evm.OpDeploy, "", s.deployGas, code)
	if err != nil {
		rl.Error("publish failed", "err", err)
		return evm.Address{}, err
	}
	addr := evm.ContractAddress(hash.Bytes())
	rl = rl.With("inscription", hash.String(), "address", addr.Hex())
	rl.Info("published, waiting for NoM confirmation")

	if _, err := s.in.WaitForConfirmation(s.ctx, hash); err != nil {
		rl.Error("not confirmed on NoM", "err", err, "elapsed", time.Since(start))
		return evm.Address{}, err
	}
	rl.Info("confirmed on NoM, waiting for the indexer to derive it")

	rec, err := s.waitIndexed(hash.String())
	if err != nil {
		rl.Error("never appeared in the indexer's EVM state", "err", err, "elapsed", time.Since(start))
		return evm.Address{}, err
	}
	if !boolField(rec, "success") {
		rl.Error("deploy reverted", "error", str(rec["error"]), "elapsed", time.Since(start))
		return evm.Address{}, fmt.Errorf("%s deploy reverted: %s", name, str(rec["error"]))
	}
	rl.Info("deploy indexed", "gasUsed", rec["gasUsed"], "elapsed", time.Since(start))
	s.ops++
	return addr, nil
}

// publish inscribes one EVM operation and burns the fee its gas requires.
//
// It goes through the same evmMeta and payment helpers as `zcribe evm deploy`
// and `zcribe evm call`, not the non-paying InscribeFile. Once a chain's
// FeeFromHeight has passed, an inscription that burns nothing gets a gas
// budget of zero. The chain still accepts the block, and the derived receipt
// just reports "out of gas" (HANDOFF.md, "What an activation height does to
// publishers"). On a chain with no fee rule, payment returns nil and this is
// exactly InscribeFile.
func (s *evmSim) publish(rl *slog.Logger, name string, op evm.Op, to string, gas uint64, content []byte) (types.Hash, error) {
	f := feeFlags{gas: &gas, token: &s.feeToken}
	meta, err := evmMeta(name, op, to, &f, s.in.ChainID())
	if err != nil {
		return types.ZeroHash, err
	}
	pay, note, err := payment(s.ctx, s.in, &f, meta, content)
	if err != nil {
		return types.ZeroHash, err
	}
	s.lastFee = nil
	if pay != nil {
		s.lastFee = pay.Amount
	}
	hash, err := s.in.InscribeFilePaying(s.ctx, meta, content, protocol.PlasmaFused, s.in.Address(), pay)
	if err == nil && note != "" {
		rl.Info("burned fee", "inscription", hash.String(), "fee", note)
	}
	return hash, err
}

// submitCall inscribes one contract call, waits for it to confirm on NoM and
// then to be indexed, and returns the receipt JSON /evm/tx/{id} would also
// serve. rl should already carry the round number, so every line this
// produces can be traced back to the round that caused it.
func (s *evmSim) submitCall(rl *slog.Logger, label string, to evm.Address, calldata []byte) (map[string]any, error) {
	rl = rl.With("op", label, "to", to.Hex())

	start := time.Now()
	row := s.rec.begin(s.wallet, label, len(calldata), start)
	hash, err := s.publish(rl, "call.evm", evm.OpCall, to.Hex(), s.callGas, calldata)
	row.published(s.lastFee, err)
	if err != nil {
		rl.Error("publish failed", "err", err)
		s.rec.add(row)
		return nil, err
	}
	rl = rl.With("inscription", hash.String())

	// A call that misses a congested momentum is rebid rather than waited on:
	// without that it stays pending until the price falls back to what it
	// paid, and the wallet is frozen behind it. The receipt is then under the
	// replacement's hash.
	landed, momentum, rebids, err := s.in.ConfirmRebidding(s.ctx, hash, s.rebidAfter)
	row.confirmed(momentum, rebids, err)
	if rebids > 0 {
		rl = rl.With("rebids", rebids, "confirmed_as", landed.String())
	}
	hash = landed
	if err != nil {
		rl.Error("not confirmed on NoM", "err", err, "elapsed", time.Since(start))
		s.rec.add(row)
		return nil, err
	}
	if s.rec != nil {
		// What the block actually paid is on the block, not in the quote.
		if b, err := s.in.Block(s.ctx, hash); err == nil && b != nil {
			row.BasePlasma, row.FusedPlasma, row.Difficulty = b.BasePlasma, b.FusedPlasma, b.Difficulty
		}
	}
	rec, err := s.waitIndexed(hash.String())
	row.indexed(rec, err)
	s.rec.add(row)
	if err != nil {
		rl.Error("never appeared in the indexer's EVM state", "err", err, "elapsed", time.Since(start))
		return nil, err
	}
	rl.Info("indexed", "success", rec["success"], "gasUsed", rec["gasUsed"],
		"error", rec["error"], "elapsed", time.Since(start))
	s.ops++
	return rec, nil
}

// waitIndexed polls /evm/tx/{id} until the derived chain has caught up to
// one inscription, or gives up after indexWait. Polling the public API
// rather than the database directly means this script sees exactly what a
// browser would — if a bug (like the ambiguous-column one) breaks reading
// EVM state back, this fails here instead of quietly passing against a
// database this run has special access to.
func (s *evmSim) waitIndexed(inscriptionID string) (map[string]any, error) {
	deadline := time.Now().Add(s.indexWait)
	for {
		select {
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		case <-time.After(2 * time.Second):
		}
		rec, err := s.get("/evm/tx/" + url.PathEscape(inscriptionID))
		if err == nil {
			return rec, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("not indexed within %s (last error: %v)", s.indexWait, err)
		}
	}
}

func (s *evmSim) get(path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, s.api+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("indexer returned %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if resp.StatusCode != http.StatusOK {
		if msg, ok := out["error"].(string); ok {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("indexer returned %d", resp.StatusCode)
	}
	return out, nil
}

func (s *evmSim) round(n int) {
	rl := s.log.With("round", n)
	if s.rng.Float64() < s.failRate {
		s.doBadCall(rl)
		return
	}
	switch s.rng.Intn(3) {
	case 0:
		s.doIncrement(rl)
	case 1:
		s.doAdd(rl)
	default:
		s.doTransfer(rl)
	}
}

func (s *evmSim) doIncrement(rl *slog.Logger) {
	calldata, _ := evm.EncodeCall("increment()")
	rec, err := s.submitCall(rl, "counter.increment", s.counter, calldata)
	if err != nil {
		s.failures++
		return
	}
	if !boolField(rec, "success") {
		rl.Error("expected increment() to succeed but it reverted", "error", str(rec["error"]))
		s.mismatches++
		return
	}
	s.expectedCounter.Add(s.expectedCounter, big.NewInt(1))
	s.verifyCounter(rl)
}

func (s *evmSim) doAdd(rl *slog.Logger) {
	n := big.NewInt(int64(1 + s.rng.Intn(1000)))
	calldata, err := evm.EncodeCall("add(uint256)", n)
	if err != nil {
		rl.Error("cannot encode add(uint256)", "err", err)
		s.failures++
		return
	}
	rec, err := s.submitCall(rl, "counter.add", s.counter, calldata)
	if err != nil {
		s.failures++
		return
	}
	if !boolField(rec, "success") {
		rl.Error("expected add(uint256) to succeed but it reverted", "error", str(rec["error"]))
		s.mismatches++
		return
	}
	s.expectedCounter.Add(s.expectedCounter, n)
	s.verifyCounter(rl)
}

func (s *evmSim) doTransfer(rl *slog.Logger) {
	bal := s.expectedBalances[s.deployer]
	if bal == nil || bal.Sign() <= 0 {
		rl.Warn("deployer has nothing left to transfer; skipping this round")
		return
	}
	to := s.recipients[s.rng.Intn(len(s.recipients))]
	// Never move more than a slice of the remaining balance, so a long run
	// cannot exhaust it on the first few transfers.
	maxUnit := new(big.Int).Div(bal, big.NewInt(50))
	if maxUnit.Sign() == 0 {
		maxUnit = bal
	}
	amount := new(big.Int).Add(big.NewInt(1), randBigInt(s.rng, maxUnit))

	calldata, err := evm.EncodeCall(evm.ERC20.Transfer, to, amount)
	if err != nil {
		rl.Error("cannot encode transfer(address,uint256)", "err", err)
		s.failures++
		return
	}
	rl = rl.With("recipient", to.Hex(), "amount", amount.String())
	rec, err := s.submitCall(rl, "erc20.transfer", s.token, calldata)
	if err != nil {
		s.failures++
		return
	}
	if !boolField(rec, "success") {
		rl.Error("expected transfer to succeed but it reverted", "error", str(rec["error"]))
		s.mismatches++
		return
	}
	s.expectedBalances[s.deployer] = new(big.Int).Sub(bal, amount)
	prev := s.expectedBalances[to]
	if prev == nil {
		prev = new(big.Int)
	}
	s.expectedBalances[to] = new(big.Int).Add(prev, amount)
	s.verifyBalances(rl)
}

// doBadCall sends four bytes that match none of the counter's selectors, so
// its own fallback reverts (see simCounterInit's comment). This is the
// negative-path check: a receipt the overview page counts as "failed" that
// this run expects, versus one that would mean the EVM's own dispatch logic
// broke.
func (s *evmSim) doBadCall(rl *slog.Logger) {
	rec, err := s.submitCall(rl, "counter.bad-selector", s.counter, []byte{0xde, 0xad, 0xbe, 0xef})
	if err != nil {
		s.failures++
		return
	}
	if boolField(rec, "success") {
		rl.Error("expected a malformed call to revert but it succeeded")
		s.mismatches++
	}
}

// verifyCounter reads the counter's storage slot 0 back through the public
// API and compares it against this run's own running total. A mismatch here
// is exactly the kind of divergence a state-root comparison between two
// indexers would also catch — this just catches it against a script's own
// arithmetic, with no second indexer required.
func (s *evmSim) verifyCounter(rl *slog.Logger) {
	got, err := s.readCounter()
	if err != nil {
		rl.Error("could not read the counter account back to verify it", "err", err)
		s.mismatches++
		return
	}
	if got.Cmp(s.expectedCounter) != 0 {
		rl.Error("counter state mismatch", "expected", s.expectedCounter.String(), "got", got.String())
		s.mismatches++
		return
	}
	rl.Info("counter verified", "value", got.String())
}

// readCounter is the counter's storage slot 0, as the public API reports it.
func (s *evmSim) readCounter() (*big.Int, error) {
	acc, err := s.get("/evm/account/" + s.counter.Hex())
	if err != nil {
		return nil, err
	}
	got := new(big.Int)
	for _, raw := range asSlice(acc["storage"]) {
		m, _ := raw.(map[string]any)
		key := strings.TrimPrefix(str(m["Key"]), "0x")
		if strings.Trim(key, "0") == "" { // slot 0
			got.SetString(str(m["Value"]), 10)
		}
	}
	return got, nil
}

// verifyBalances reads the token's holder list back and compares every
// balance this run has moved against what it expects. A holder this run
// never touched is not checked — only the addresses this script itself has
// paid are its responsibility to verify.
func (s *evmSim) verifyBalances(rl *slog.Logger) {
	data, err := s.get("/evm/token/" + s.token.Hex())
	if err != nil {
		rl.Error("could not read token holders back to verify balances", "err", err)
		s.mismatches++
		return
	}
	got := map[string]string{}
	for _, raw := range asSlice(data["holders"]) {
		m, _ := raw.(map[string]any)
		got[strings.ToLower(str(m["address"]))] = str(m["balance"])
	}
	for addr, want := range s.expectedBalances {
		if want.Sign() == 0 {
			continue // a zeroed-out holder legitimately drops off the list
		}
		key := strings.ToLower(addr.Hex())
		gotStr, ok := got[key]
		if !ok {
			rl.Error("balance missing from holder list", "address", addr.Hex(), "expected", want.String())
			s.mismatches++
			continue
		}
		if gotStr != want.String() {
			rl.Error("balance mismatch", "address", addr.Hex(), "expected", want.String(), "got", gotStr)
			s.mismatches++
		}
	}
	rl.Info("balances verified", "holders", len(got))
}

func (s *evmSim) summarize() {
	fields := []any{"operations", s.ops, "failures", s.failures, "mismatches", s.mismatches}
	if overview, err := s.get("/evm/overview"); err == nil {
		fields = append(fields, "chainTxs", overview["txs"], "chainFailed", overview["failed"], "stateRoot", overview["stateRoot"])
	}
	if s.failures == 0 && s.mismatches == 0 {
		s.log.Info("simulation completed cleanly", fields...)
		return
	}
	s.log.Error("simulation finished with problems — see the run's log lines above for exactly which round and inscription", fields...)
}

func boolField(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func randBigInt(rng *rand.Rand, max *big.Int) *big.Int {
	if max.Sign() <= 0 {
		return new(big.Int)
	}
	// Uniform in [0, max]. Token amounts are 18-decimal and routinely exceed
	// int64, which the old Int63n path could not draw from — it returned max
	// every time, so transfers were never actually random.
	return new(big.Int).Rand(rng, new(big.Int).Add(max, big.NewInt(1)))
}
