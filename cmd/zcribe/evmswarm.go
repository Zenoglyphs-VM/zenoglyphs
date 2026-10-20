package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/evm"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/inscribe"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
)

// zcribe evm swarm is evm simulate with many senders at once.
//
// evm simulate has one wallet, so every call it makes is strictly ordered by
// that wallet's own account chain. It can never produce the case a real chain
// produces constantly: several accounts' calls landing in the same momentum,
// touching the same contract, with the canonical (height, index) order the
// only thing deciding which ran first. That is the case the deriver's
// ordering exists for, so it is worth generating on purpose.
//
// The swarm wallets are derived from the same seed as -index, at the indexes
// after it (or from -swarm-index), and the -index wallet funds them: fused
// QSR for plasma, fee tokens to burn, and ERC-20 balance to move. Then every wallet runs its
// rounds at the same time against one counter and one token. Transfers go to
// other swarm wallets as well as to the synthetic recipients, so balances are
// being credited by some senders while being spent by others.
//
// State is verified once, at the end, and not after each call as evm simulate
// does: mid-run, the API legitimately reflects other wallets' calls that this
// run has not yet counted, so a per-call check would report mismatches that
// are only races in the checker. Every call's own receipt is still checked as
// it lands.
func evmSwarm(ctx context.Context, args []string) error {
	set := fs("evm swarm")
	var w walletFlags
	w.register(set)
	var sf simFlags
	sf.register(set)
	wallets := set.Int("wallets", 8, "swarm wallets acting at the same time")
	firstIndex := set.Uint("swarm-index", 0, "account index of the first swarm wallet, under the same seed as -index; 0 means the one after -index")
	rounds := set.Int("rounds", 5, "operations each swarm wallet runs")
	fundFlag := set.String("fund", "0.5", "fee token (see -fee-token) each swarm wallet is topped up to before the run, in whole units; raised automatically if -rounds needs more")
	tokensFlag := set.String("tokens", "1000", "ERC-20 balance each swarm wallet is topped up to from the deployer, in whole tokens (18 decimals)")
	plasmaFlag := set.Uint64("plasma-qsr", 1000, "QSR the funder fuses for each swarm wallet's plasma, so it never falls back to proof of work; 0 skips fusing")
	duration := set.Duration("duration", 0, "run for this long instead of -rounds: every wallet keeps starting rounds until it passes, and finishes the one in flight")
	reportPath := set.String("report", "", "write every call's timings, costs and outcome to this JSON file")
	_ = set.Parse(args)

	// A time-bound run still has to be funded for the rounds it might run. A
	// call has never taken less than a few seconds end to end, so one per
	// three seconds is a ceiling, and coverRounds doubles it again.
	budgetRounds := *rounds
	if *duration > 0 {
		budgetRounds = int(*duration/(3*time.Second)) + 1
	}
	var rec *benchRecorder
	if *reportPath != "" {
		rec = &benchRecorder{}
	}

	if *wallets < 1 {
		return fmt.Errorf("-wallets must be at least 1")
	}
	first := *firstIndex
	if first == 0 {
		first = w.index + 1
	}
	if w.index >= first && w.index < first+uint(*wallets) {
		return fmt.Errorf("-index %d is inside the swarm's range %d..%d; the funder cannot also be a swarm wallet",
			w.index, first, first+uint(*wallets)-1)
	}
	feeTarget, err := parseUnits(*fundFlag, 8)
	if err != nil {
		return fmt.Errorf("-fund: %w", err)
	}
	tokenTarget, err := parseUnits(*tokensFlag, 18)
	if err != nil {
		return fmt.Errorf("-tokens: %w", err)
	}
	_, feeZTS, err := (&feeFlags{token: sf.feeToken}).standard()
	if err != nil {
		return err
	}

	runID := hex.EncodeToString(randBytes(4))
	log := newLogger(w.verbose).With("run", runID, "component", "evm-swarm")
	log.Info("starting EVM swarm simulation", "wallets", *wallets, "rounds", *rounds,
		"first_index", first, "seed", *sf.seed, "api", *sf.api, "fail_rate", *sf.failRate)

	funder, err := w.inscriber(ctx)
	if err != nil {
		log.Error("cannot build funding wallet", "err", err)
		return fmt.Errorf("wallet: %w", err)
	}
	log.Info("funding wallet ready", "address", funder.Address(), "index", w.index)

	base, err := sf.newSim(ctx, funder, log)
	if err != nil {
		return err
	}
	if err := base.setup(*sf.counter, *sf.token); err != nil {
		log.Error("setup failed", "err", err)
		return err
	}

	src, err := w.source()
	if err != nil {
		return err
	}
	sw := &swarm{
		ctx: ctx, base: base, client: rpcclient.New(w.node),
		led:    &swarmLedger{balances: map[evm.Address]*big.Int{}},
		feeZTS: feeZTS, feeTarget: feeTarget, tokenTarget: tokenTarget,
		plasmaTarget: *plasmaFlag * constants.PlasmaPerFusionUnit,
	}
	if err := sw.coverRounds(budgetRounds, log); err != nil {
		return err
	}
	for i := 1; i <= *wallets; i++ {
		src.Index = uint32(first) + uint32(i-1)
		key, err := inscribe.LoadKey(src)
		if err != nil {
			return fmt.Errorf("derive swarm wallet %d: %w", i, err)
		}
		in, err := inscribe.New(sw.client, key, funder.ChainID(), newLogger(w.verbose))
		if err != nil {
			return err
		}
		in.HeadroomPercent = *sf.headroom
		addr, err := evm.AddressOf(in.Address().String())
		if err != nil {
			return fmt.Errorf("map swarm wallet %d into the EVM: %w", i, err)
		}
		// Each wallet gets its own copy of the simulator: its own inscriber,
		// logger and counters, and its own rng, because math/rand's Rand is
		// not safe to share between goroutines. The expected-state fields are
		// cleared so nothing can update them behind the shared ledger's lock.
		sim := *base
		sim.in = in
		sim.log = log.With("wallet", i, "address", in.Address())
		sim.rng = rand.New(rand.NewSource(*sf.seed + int64(i)))
		sim.ops, sim.failures, sim.mismatches = 0, 0, 0
		sim.expectedCounter, sim.expectedBalances = nil, nil
		sim.rec, sim.wallet = rec, i
		sw.members = append(sw.members, &swarmMember{n: i, in: in, evm: addr, sim: &sim})
		log.Info("swarm wallet", "wallet", i, "index", src.Index, "address", in.Address())
	}

	if err := sw.seedLedger(); err != nil {
		log.Error("cannot read starting state", "err", err)
		return err
	}
	fundStart := time.Now()
	if err := sw.fund(); err != nil {
		log.Error("funding failed", "err", err)
		return err
	}

	// The start barrier. Funding is sequential by necessity — one funder, one
	// account chain — but from here on nothing is: every wallet is released
	// at once, and each publishes on its own account chain.
	start := make(chan struct{})
	var wg sync.WaitGroup
	loadStart := time.Now()
	if *duration > 0 {
		sw.deadline = loadStart.Add(*duration)
	}
	for _, m := range sw.members {
		wg.Add(1)
		go func(m *swarmMember) {
			defer wg.Done()
			<-start
			sw.run(m, *rounds)
		}(m)
	}
	log.Info("swarm funded, releasing every wallet at once", "wallets", len(sw.members), "duration", *duration)
	close(start)
	wg.Wait()
	loadEnd := time.Now()

	// Every call each wallet made has been waited on until indexed, so once
	// all of them return, the API's state includes every one of them.
	base.expectedCounter = sw.led.counter
	base.expectedBalances = sw.led.balances
	final := log.With("phase", "verify")
	base.verifyCounter(final)
	base.verifyBalances(final)
	for _, m := range sw.members {
		base.ops += m.sim.ops
		base.failures += m.sim.failures
		base.mismatches += m.sim.mismatches
	}
	base.summarize()
	if rec != nil {
		rep := benchReport{
			Run: runID, Wallets: len(sw.members), FailRate: *sf.failRate,
			CallGas: base.callGas, FeeToken: base.feeToken,
			Counter: base.counter.Hex(), Token: base.token.Hex(),
			FundStart: fundStart.UnixMilli(), LoadStart: loadStart.UnixMilli(), LoadEnd: loadEnd.UnixMilli(),
			Failures: base.failures, Mismatches: base.mismatches,
		}
		if *duration > 0 {
			rep.Duration = duration.String()
		} else {
			rep.Rounds = *rounds
		}
		if err := rec.write(*reportPath, rep); err != nil {
			log.Error("cannot write report", "path", *reportPath, "err", err)
		} else {
			log.Info("report written", "path", *reportPath)
		}
	}
	if base.failures > 0 || base.mismatches > 0 {
		return fmt.Errorf("swarm finished with %d unexpected failure(s) and %d state mismatch(es) — see run=%s above for exactly which wallet, round and inscription",
			base.failures, base.mismatches, runID)
	}
	return nil
}

type swarm struct {
	ctx     context.Context
	base    *evmSim // the funder, which also deploys and verifies
	client  *rpcclient.Client
	members []*swarmMember
	led     *swarmLedger

	feeZTS      types.ZenonTokenStandard
	feeTarget   *big.Int
	tokenTarget *big.Int
	// deadline, when set, replaces the round count: no wallet starts a round
	// after it.
	deadline time.Time
	// plasmaTarget is the fused plasma each wallet should be entitled to.
	// PlasmaPerFusionUnit is per whole QSR (CostPerFusionUnit is 1e8 raw).
	plasmaTarget uint64
}

type swarmMember struct {
	n   int
	in  *inscribe.Inscriber
	evm evm.Address
	sim *evmSim
}

// swarmLedger is the run's own arithmetic, shared by every wallet.
//
// A sender's balance is debited when its transfer is *published*, not when
// it is confirmed. Only that wallet ever spends from its balance, so debiting
// early means it can never publish a transfer it cannot cover, however many
// incoming credits are still in flight. A transfer that reverts gets its
// amount back; one that succeeds credits the recipient.
type swarmLedger struct {
	mu       sync.Mutex
	counter  *big.Int
	balances map[evm.Address]*big.Int
}

func (l *swarmLedger) addCounter(n *big.Int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counter = new(big.Int).Add(l.counter, n)
}

func (l *swarmLedger) balance(a evm.Address) *big.Int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.balances[a]; b != nil {
		return new(big.Int).Set(b)
	}
	return new(big.Int)
}

func (l *swarmLedger) credit(a evm.Address, n *big.Int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := l.balances[a]
	if prev == nil {
		prev = new(big.Int)
	}
	l.balances[a] = new(big.Int).Add(prev, n)
}

// reserve picks a transfer amount of at most a tenth of from's balance and
// debits it, or returns nil if from holds nothing.
func (l *swarmLedger) reserve(from evm.Address, rng *rand.Rand) *big.Int {
	l.mu.Lock()
	defer l.mu.Unlock()
	bal := l.balances[from]
	if bal == nil || bal.Sign() <= 0 {
		return nil
	}
	maxUnit := new(big.Int).Div(bal, big.NewInt(10))
	if maxUnit.Sign() == 0 {
		maxUnit = new(big.Int).Set(bal)
	}
	amount := new(big.Int).Add(big.NewInt(1), randBigInt(rng, new(big.Int).Sub(maxUnit, big.NewInt(1))))
	l.balances[from] = new(big.Int).Sub(bal, amount)
	return amount
}

// seedLedger starts the shared ledger from what the API reports, so a reused
// token or counter carrying earlier runs' state is the baseline rather than a
// guaranteed mismatch.
func (sw *swarm) seedLedger() error {
	sw.led.counter = new(big.Int).Set(sw.base.expectedCounter)

	data, err := sw.base.get("/evm/token/" + sw.base.token.Hex())
	if err != nil {
		return fmt.Errorf("read token holders: %w", err)
	}
	held := map[string]string{}
	for _, raw := range asSlice(data["holders"]) {
		m, _ := raw.(map[string]any)
		held[strings.ToLower(str(m["address"]))] = str(m["balance"])
	}
	addrs := append([]evm.Address{sw.base.deployer}, sw.base.recipients...)
	for _, m := range sw.members {
		addrs = append(addrs, m.evm)
	}
	for _, a := range addrs {
		bal := new(big.Int)
		s, ok := held[strings.ToLower(a.Hex())]
		switch {
		case ok:
			if _, ok := bal.SetString(s, 10); !ok {
				return fmt.Errorf("holder %s has an unreadable balance %q", a.Hex(), s)
			}
		case a == sw.base.deployer && sw.base.expectedBalances[a] != nil:
			// The holder list is rebuilt from Transfer events, and the
			// fixture's constructor mints the whole supply without emitting
			// one. So until the deployer's first transfer it is not listed at
			// all, and reading its absence as zero is what made every swarm
			// against a fresh token refuse to fund. setup already knows what
			// it minted.
			bal.Set(sw.base.expectedBalances[a])
		}
		sw.led.balances[a] = bal
	}
	return nil
}

// coverRounds raises the fee target to what -rounds will actually burn.
//
// -fund is a floor, not a promise: one call's fee is quoted from the gas it
// buys and the resource price of the moment, so a fixed 0.5 ZNN covers five
// rounds comfortably and a hundred not at all — and a wallet that runs dry
// mid-run fails its publishes as "insufficient balance", which reads like a
// swarm bug. Twice the quoted total leaves room for the price to move.
func (sw *swarm) coverRounds(rounds int, log *slog.Logger) error {
	calldata, err := evm.EncodeCall(evm.ERC20.Transfer, sw.base.deployer, sw.tokenTarget)
	if err != nil {
		return err
	}
	f := feeFlags{gas: &sw.base.callGas, token: &sw.base.feeToken}
	meta, err := evmMeta("call.evm", evm.OpCall, sw.base.token.Hex(), &f, sw.base.in.ChainID())
	if err != nil {
		return err
	}
	pay, _, err := payment(sw.ctx, sw.base.in, &f, meta, calldata)
	if err != nil {
		return fmt.Errorf("quote a call's fee: %w", err)
	}
	if pay == nil {
		return nil // no fee rule on this chain; plasma is the only cost
	}
	need := new(big.Int).Mul(pay.Amount, big.NewInt(int64(2*rounds)))
	if need.Cmp(sw.feeTarget) > 0 {
		log.Info("raising the fee target to cover every round", "per_call", pay.Amount.String(),
			"rounds", rounds, "from", sw.feeTarget.String(), "to", need.String())
		sw.feeTarget = need
	}
	return nil
}

// fund tops every swarm wallet up to -fund in the fee token and -tokens in
// the ERC-20.
//
// Everything the funder sends is published back-to-back, and only then are
// the confirmations awaited. That is safe because go-zenon's frontier
// includes an account's unconfirmed blocks (chain/account_pool.go), so each
// send builds on the one before it, up to the node's limit of 500 uncommitted
// blocks per account. It costs roughly one momentum of waiting instead of one
// per wallet.
func (sw *swarm) fund() error {
	ctx, funder := sw.ctx, sw.base.in
	log := sw.base.log.With("phase", "fund")

	var feeSends []types.Hash
	for _, m := range sw.members {
		have, err := sw.feeBalance(m.in.Address())
		if err != nil {
			return fmt.Errorf("wallet %d fee balance: %w", m.n, err)
		}
		need := new(big.Int).Sub(sw.feeTarget, have)
		if need.Sign() <= 0 {
			log.Info("already holds enough to pay fees", "wallet", m.n, "balance", have.String())
			continue
		}
		hash, err := funder.Send(ctx, m.in.Address(), need, sw.feeZTS, nil)
		if err != nil {
			return fmt.Errorf("send fee money to wallet %d: %w", m.n, err)
		}
		log.Info("sent fee money", "wallet", m.n, "amount", need.String(), "token", sw.feeZTS.String(), "hash", hash.String())
		feeSends = append(feeSends, hash)
	}

	fuses, err := sw.fuse(log)
	if err != nil {
		return err
	}

	type tokenSend struct {
		m      *swarmMember
		amount *big.Int
		hash   types.Hash
	}
	var sends []tokenSend
	total := new(big.Int)
	for _, m := range sw.members {
		if need := new(big.Int).Sub(sw.tokenTarget, sw.led.balance(m.evm)); need.Sign() > 0 {
			sends = append(sends, tokenSend{m: m, amount: need})
			total.Add(total, need)
		}
	}
	if have := sw.led.balance(sw.base.deployer); have.Cmp(total) < 0 {
		return fmt.Errorf("the deployer holds %s token units but the swarm needs %s more; lower -tokens or -wallets", have, total)
	}
	for i := range sends {
		calldata, err := evm.EncodeCall(evm.ERC20.Transfer, sends[i].m.evm, sends[i].amount)
		if err != nil {
			return fmt.Errorf("encode funding transfer: %w", err)
		}
		hash, err := sw.base.publish(log, "call.evm", evm.OpCall, sw.base.token.Hex(), sw.base.callGas, calldata)
		if err != nil {
			return fmt.Errorf("publish funding transfer to wallet %d: %w", sends[i].m.n, err)
		}
		sends[i].hash = hash
		log.Info("sent tokens", "wallet", sends[i].m.n, "amount", sends[i].amount.String(), "inscription", hash.String())
	}

	for _, h := range feeSends {
		if _, err := funder.WaitForConfirmation(ctx, h); err != nil {
			return fmt.Errorf("fee send %s: %w", h, err)
		}
	}
	// Plasma before receiving, so the receives below are paid from it rather
	// than each wallet mining its way into its own first block.
	if err := sw.awaitPlasma(fuses, log); err != nil {
		return err
	}
	// A send only lands in the recipient's balance once the recipient
	// publishes a receive for it, so every wallet takes delivery — in
	// parallel, since each receive is on the receiving wallet's own chain.
	errs := make([]error, len(sw.members))
	var wg sync.WaitGroup
	for i, m := range sw.members {
		wg.Add(1)
		go func(i int, m *swarmMember) {
			defer wg.Done()
			errs[i] = sw.settle(m)
		}(i, m)
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}

	for _, s := range sends {
		if _, err := funder.WaitForConfirmation(ctx, s.hash); err != nil {
			return fmt.Errorf("token funding %s: %w", s.hash, err)
		}
		rec, err := sw.base.waitIndexed(s.hash.String())
		if err != nil {
			return fmt.Errorf("token funding %s: %w", s.hash, err)
		}
		if !boolField(rec, "success") {
			return fmt.Errorf("token funding to wallet %d reverted: %s", s.m.n, str(rec["error"]))
		}
		sw.led.credit(sw.base.deployer, new(big.Int).Neg(s.amount))
		sw.led.credit(s.m.evm, s.amount)
		sw.base.ops++
	}
	log.Info("every swarm wallet funded", "fee_sends", len(feeSends), "fusions", len(fuses), "token_sends", len(sends))
	return nil
}

// fuse locks QSR in the plasma contract on behalf of every swarm wallet that
// is entitled to less than plasmaTarget.
//
// Without it a fresh swarm wallet has no plasma at all, and every block it
// publishes — its receives, every call — is paid for with proof of work. That
// still confirms, which is why the gap was easy to miss, but it turns a swarm
// meant to measure the deriver into one that measures eight CPUs mining, and
// PoW caps a block's data at 1,080 bytes besides. Fusion names a beneficiary,
// so the funder pays from its own chain and the swarm wallets never receive
// anything; the QSR stays the funder's and can be cancelled back after
// FuseExpiration.
func (sw *swarm) fuse(log *slog.Logger) (map[*swarmMember]uint64, error) {
	out := map[*swarmMember]uint64{}
	if sw.plasmaTarget == 0 {
		return out, nil
	}
	if sw.plasmaTarget > constants.MaxFusionPlasmaForAccount {
		return nil, fmt.Errorf("-plasma-qsr is above the %d QSR an account can usefully fuse",
			constants.MaxFusionUnitsPerAccount)
	}
	type fusion struct {
		m     *swarmMember
		units uint64
	}
	var plan []fusion
	total := new(big.Int)
	for _, m := range sw.members {
		p, err := sw.client.Plasma(sw.ctx, m.in.Address())
		if err != nil {
			return nil, fmt.Errorf("wallet %d plasma: %w", m.n, err)
		}
		if p.MaxPlasma >= sw.plasmaTarget {
			log.Info("already has enough plasma", "wallet", m.n, "plasma", p.MaxPlasma)
			continue
		}
		units := (sw.plasmaTarget - p.MaxPlasma + constants.PlasmaPerFusionUnit - 1) / constants.PlasmaPerFusionUnit
		if min := constants.FuseMinAmount.Uint64() / constants.CostPerFusionUnit; units < min {
			units = min
		}
		plan = append(plan, fusion{m, units})
		total.Add(total, new(big.Int).SetUint64(units*constants.CostPerFusionUnit))
	}
	if len(plan) == 0 {
		return out, nil
	}
	have, err := sw.client.Balance(sw.ctx, sw.base.in.Address(), types.QsrTokenStandard)
	if err != nil {
		return nil, fmt.Errorf("funder QSR balance: %w", err)
	}
	if have.Cmp(total) < 0 {
		return nil, fmt.Errorf("fusing plasma for the swarm needs %s raw QSR and the funder holds %s; lower -plasma-qsr or -wallets, or pass -plasma-qsr 0 to pay with proof of work",
			total, have)
	}
	for _, f := range plan {
		data, err := definition.ABIPlasma.PackMethod(definition.FuseMethodName, f.m.in.Address())
		if err != nil {
			return nil, fmt.Errorf("encode fuse: %w", err)
		}
		amount := new(big.Int).SetUint64(f.units * constants.CostPerFusionUnit)
		hash, err := sw.base.in.Send(sw.ctx, types.PlasmaContract, amount, types.QsrTokenStandard, data)
		if err != nil {
			return nil, fmt.Errorf("fuse plasma for wallet %d: %w", f.m.n, err)
		}
		log.Info("fused plasma", "wallet", f.m.n, "qsr", f.units, "plasma", f.units*constants.PlasmaPerFusionUnit, "hash", hash.String())
		out[f.m] = f.units * constants.PlasmaPerFusionUnit
	}
	return out, nil
}

// awaitPlasma waits until every fused wallet's plasma shows. A fuse is a send
// to the plasma contract, and the entitlement only exists once the contract's
// own receive for it is in a momentum — a beat after the send confirms.
func (sw *swarm) awaitPlasma(fuses map[*swarmMember]uint64, log *slog.Logger) error {
	deadline := time.Now().Add(sw.base.indexWait)
	for m := range fuses {
		for {
			p, err := sw.client.Plasma(sw.ctx, m.in.Address())
			if err == nil && p.MaxPlasma >= sw.plasmaTarget {
				log.Info("plasma ready", "wallet", m.n, "plasma", p.MaxPlasma)
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("wallet %d's fused plasma never appeared within %s", m.n, sw.base.indexWait)
			}
			select {
			case <-sw.ctx.Done():
				return sw.ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return nil
}

// feeBalance is what an address holds in the fee token, including sends it
// has not received yet — an earlier run that stopped between sending and
// receiving should not be paid twice.
func (sw *swarm) feeBalance(addr types.Address) (*big.Int, error) {
	bal, err := sw.client.Balance(sw.ctx, addr, sw.feeZTS)
	if err != nil {
		return nil, err
	}
	total := new(big.Int).Set(bal)
	pending, err := sw.client.UnreceivedBlocks(sw.ctx, addr, 0, 50)
	if err != nil {
		return nil, err
	}
	for _, b := range pending {
		if b.TokenStandard == sw.feeZTS && b.Amount.Int != nil {
			total.Add(total, b.Amount.Int)
		}
	}
	return total, nil
}

// settle receives everything waiting for one wallet until its fee balance
// reaches the target. It polls rather than receiving once, because a send is
// listed as unreceived only after the momentum confirming it, and that can
// trail the confirmation the funder saw by a moment.
func (sw *swarm) settle(m *swarmMember) error {
	deadline := time.Now().Add(sw.base.indexWait)
	for {
		if bal, err := sw.client.Balance(sw.ctx, m.in.Address(), sw.feeZTS); err == nil && bal.Cmp(sw.feeTarget) >= 0 {
			return nil
		}
		pending, err := sw.client.UnreceivedBlocks(sw.ctx, m.in.Address(), 0, 50)
		if err != nil {
			return fmt.Errorf("wallet %d unreceived: %w", m.n, err)
		}
		var last types.Hash
		for _, b := range pending {
			h, err := m.in.Receive(sw.ctx, b.Hash)
			if err != nil {
				return fmt.Errorf("wallet %d receive %s: %w", m.n, b.Hash, err)
			}
			last = h
		}
		if last != types.ZeroHash {
			if _, err := m.in.WaitForConfirmation(sw.ctx, last); err != nil {
				return fmt.Errorf("wallet %d receive: %w", m.n, err)
			}
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wallet %d never reached a fee balance of %s within %s", m.n, sw.feeTarget, sw.base.indexWait)
		}
		select {
		case <-sw.ctx.Done():
			return sw.ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// run is one wallet's share of the swarm: the same mix of operations as
// evm simulate, against the same shared contracts as every other wallet.
func (sw *swarm) run(m *swarmMember, rounds int) {
	s := m.sim
	timed := !sw.deadline.IsZero()
	for i := 1; timed || i <= rounds; i++ {
		if sw.ctx.Err() != nil || (timed && time.Now().After(sw.deadline)) {
			return
		}
		rl := s.log.With("round", i)
		if s.rng.Float64() < s.failRate {
			s.doBadCall(rl)
			continue
		}
		switch s.rng.Intn(3) {
		case 0:
			calldata, _ := evm.EncodeCall("increment()")
			sw.counterOp(m, rl, "counter.increment", calldata, big.NewInt(1))
		case 1:
			n := big.NewInt(int64(1 + s.rng.Intn(1000)))
			calldata, err := evm.EncodeCall("add(uint256)", n)
			if err != nil {
				rl.Error("cannot encode add(uint256)", "err", err)
				s.failures++
				continue
			}
			sw.counterOp(m, rl, "counter.add", calldata, n)
		default:
			sw.transfer(m, rl)
		}
	}
}

func (sw *swarm) counterOp(m *swarmMember, rl *slog.Logger, label string, calldata []byte, delta *big.Int) {
	rec, err := m.sim.submitCall(rl, label, m.sim.counter, calldata)
	if err != nil {
		m.sim.failures++
		return
	}
	if !boolField(rec, "success") {
		rl.Error("expected "+label+" to succeed but it reverted", "error", str(rec["error"]))
		m.sim.mismatches++
		return
	}
	sw.led.addCounter(delta)
}

func (sw *swarm) transfer(m *swarmMember, rl *slog.Logger) {
	to := sw.pickRecipient(m)
	amount := sw.led.reserve(m.evm, m.sim.rng)
	if amount == nil {
		rl.Warn("wallet has nothing left to transfer; skipping this round")
		return
	}
	calldata, err := evm.EncodeCall(evm.ERC20.Transfer, to, amount)
	if err != nil {
		sw.led.credit(m.evm, amount)
		rl.Error("cannot encode transfer(address,uint256)", "err", err)
		m.sim.failures++
		return
	}
	rl = rl.With("recipient", to.Hex(), "amount", amount.String())
	rec, err := m.sim.submitCall(rl, "erc20.transfer", m.sim.token, calldata)
	if err != nil {
		// Whether this landed is unknown: the publish may have gone through
		// before a confirmation or indexing wait timed out. The reservation
		// stays debited and the recipient is not credited; the run already
		// counts as failed, and the final balance check shows which way it went.
		m.sim.failures++
		return
	}
	if !boolField(rec, "success") {
		sw.led.credit(m.evm, amount)
		rl.Error("expected transfer to succeed but it reverted", "error", str(rec["error"]))
		m.sim.mismatches++
		return
	}
	sw.led.credit(to, amount)
}

// pickRecipient chooses any other swarm wallet or a synthetic recipient.
// Sending to other swarm wallets is the point: it means some wallets are
// receiving tokens while they are also spending them.
func (sw *swarm) pickRecipient(m *swarmMember) evm.Address {
	n := len(sw.members) - 1 + len(sw.base.recipients)
	k := m.sim.rng.Intn(n)
	for _, other := range sw.members {
		if other == m {
			continue
		}
		if k == 0 {
			return other.evm
		}
		k--
	}
	return sw.base.recipients[k]
}

// parseUnits converts a decimal amount such as "0.5" into raw units.
func parseUnits(s string, decimals int) (*big.Int, error) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok || r.Sign() < 0 {
		return nil, fmt.Errorf("%q is not a non-negative amount", s)
	}
	r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)))
	if !r.IsInt() {
		return nil, fmt.Errorf("%q has more than %d decimal places", s, decimals)
	}
	return new(big.Int).Set(r.Num()), nil
}
