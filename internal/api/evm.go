package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/evm"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/inscribe"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
)

// The EVM explorer API.
//
// The inscriptions are the state, and this file is a view over a replay of
// them; the derived chain it serves is maintained in evmcache.go. Nothing the
// API persists (receipts, checkpoints) is a source: all of it is a projection
// the store can drop and the cache will rebuild.

func (s *Server) evmRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /evm/overview", s.handleEVMOverview)
	mux.HandleFunc("GET /evm/accounts", s.handleEVMAccounts)
	mux.HandleFunc("GET /evm/account/{addr}", s.handleEVMAccount)
	mux.HandleFunc("GET /evm/txs", s.handleEVMTxs)
	mux.HandleFunc("GET /evm/tx/{id}", s.handleEVMTx)
	mux.HandleFunc("GET /evm/blocks", s.handleEVMBlocks)
	mux.HandleFunc("GET /evm/block/{height}", s.handleEVMBlock)
	mux.HandleFunc("GET /evm/transfers", s.handleEVMTransfers)
	mux.HandleFunc("GET /evm/token/{addr}", s.handleEVMToken)
	mux.HandleFunc("GET /evm/fee", s.handleEVMFee)
	mux.HandleFunc("POST /evm/faucet", s.handleEVMFaucet)
}

// evmFaucet is the operator-configured signing key and token behind the
// public "receive test tokens" button. A Server whose faucet is nil answers
// /evm/faucet with 501, the same opt-out-by-default shape as the admin panel
// in admin.go: nothing here runs until EnableEVMFaucet is called at startup.
type evmFaucet struct {
	in     *inscribe.Inscriber
	token  evm.Address
	amount *big.Int // raw units sent per request
}

// EnableEVMFaucet turns on POST /evm/faucet. Call it once at startup with a
// funded key; leaving it uncalled is how an operator opts out entirely.
//
// There is deliberately no rate limiting here yet — every request this key
// signs is a real transfer, so an operator who enables this on a public
// instance is choosing to expose it to draining, not just to typos.
func (s *Server) EnableEVMFaucet(in *inscribe.Inscriber, token evm.Address, amount *big.Int) {
	s.faucet = &evmFaucet{in: in, token: token, amount: amount}
}

type evmFaucetRequest struct {
	To string `json:"to"`
}

// faucetGas is what a faucet transfer buys itself.
//
// An ERC-20 transfer measures at 13,244 gas; the headroom covers a token whose
// transfer does more, and costs the operator a fraction of a cent either way.
const faucetGas = 60_000

// faucetPayment quotes the fee for a faucet call, or nil where no fee is due.
//
// The operator burns their own ZNN for every request, which is worth being
// explicit about: with no rate limiting, "anyone who can reach the API can
// drain it" now means the ZNN as well as the tokens.
func (s *Server) faucetPayment(ctx context.Context, c *evm.Chain, meta protocol.MintWire, content []byte) (*inscribe.Payment, error) {
	if c.FeeFrom == 0 {
		return nil, nil
	}
	q, err := s.faucet.in.Quote(ctx, meta, content, protocol.PlasmaFused)
	if err != nil {
		return nil, err
	}
	fee := c.FeeFor(faucetGas, evm.ZnnStandard, time.Now().Unix(),
		evm.ResourcePrice{Base: q.BasePlasma, Paid: q.PaidPlasma})
	if fee.Sign() <= 0 {
		return nil, fmt.Errorf("quoted %s for %d gas", fee, faucetGas)
	}
	return &inscribe.Payment{
		To:     protocol.BurnAddress,
		Amount: fee,
		Token:  types.ZnnTokenStandard,
	}, nil
}

// handleEVMFaucet sends the configured amount of the configured ERC-20 to a
// caller-supplied EVM address, as one inscribed contract call signed by the
// operator's faucet key. It is intentionally a single fire-and-forget
// inscription: waiting here for the indexer to catch up would tie up an HTTP
// request for as long as the next momentum takes, and the caller can already
// watch the returned inscription id turn into a receipt at /evm/tx/{id}.
func (s *Server) handleEVMFaucet(w http.ResponseWriter, r *http.Request) {
	if s.faucet == nil {
		writeErr(w, http.StatusNotImplemented, "the EVM faucet is not enabled on this indexer")
		return
	}
	// The derived chain, for its fee rule. Read up front rather than at the
	// point of use so a faucet on an unreachable database fails before it
	// signs anything.
	//
	// The lock is released at once: the fee rule and supply curve are fixed
	// when a chain is built and never written after, so they stay readable,
	// and this handler goes on to wait for the node — holding the read side
	// through that would stall every tail behind a network round trip.
	c, done, err := s.evmRead()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	done()
	var req evmFaucetRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, `expected a JSON body like {"to":"0x..."}`)
		return
	}
	to, err := evm.ParseAddress(req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "not an EVM address: "+req.To)
		return
	}
	// The recipient has to be a NoM user account, or the tokens land somewhere
	// nobody can ever spend them from.
	//
	// This is not hypothetical: until 2026-09-10 this faucet read its address
	// from MetaMask, which produces an Ethereum keypair address — 20 bytes, so
	// it parsed and the transfer succeeded, and the balance was stranded on
	// arrival because no NoM key can sign an inscription from it. The addresses
	// being the same width is exactly what made the mistake invisible.
	if !to.Sendable() {
		writeErr(w, http.StatusBadRequest,
			to.Zenon()+" is not a Zenon account (its namespace is "+to.Kind()+
				"), so nothing could ever be spent from it. "+
				"Connect Syrius, or use a z1q… address.")
		return
	}

	calldata, err := evm.EncodeCall(evm.ERC20.Transfer, to, s.faucet.amount)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// The fee, if this chain requires one. A faucet is a publisher like any
	// other and gets no exemption — the deriver has no idea who published a
	// call, only what its carrier block paid.
	//
	// This was missed when the fee was switched on, and the failure was
	// perfectly quiet: every faucet request kept returning an inscription id
	// and a 200, and every one of them derived to `out of gas` with a budget
	// of zero. The lesson is that a publisher which does not quote is a
	// publisher that silently stops working at an activation height.
	ext := map[string]any{"op": string(evm.OpCall), "to": s.faucet.token.Hex()}
	if evm.FeeFromHeight[s.evmChainID()] != 0 {
		ext["gas"] = faucetGas
	}
	extBytes, err := protocol.Marshal(map[string]any{"evm": ext})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	meta := protocol.MintWire{Name: "faucet.evm", Mime: evm.Mime, Supply: 1, Ext: extBytes}

	pay, err := s.faucetPayment(r.Context(), c, meta, calldata)
	if err != nil {
		s.log.Error("evm faucet could not price its own call", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not price the call: "+err.Error())
		return
	}

	hash, err := s.faucet.in.InscribeFilePaying(r.Context(), meta, calldata,
		protocol.PlasmaFused, s.faucet.in.Address(), pay)
	if err != nil {
		s.log.Error("evm faucet send failed", "to", req.To, "err", err)
		writeErr(w, http.StatusInternalServerError, "send failed: "+err.Error())
		return
	}
	burned := ""
	if pay != nil {
		burned = pay.Amount.String()
	}
	s.log.Info("evm faucet sent", "to", to.Zenon(), "inscription", hash.String(),
		"amount", s.faucet.amount.String(), "burned", burned)
	writeJSON(w, http.StatusOK, map[string]any{
		"inscription": hash.String(),
		"to":          to.Hex(),
		"toZenon":     to.Zenon(),
		"token":       s.faucet.token.Hex(),
		"tokenZenon":  s.faucet.token.Zenon(),
		"amount":      s.faucet.amount.String(),
		"feeBurned":   burned,
	})
}

// --- wire types ---

type evmAccountJSON struct {
	// Address is the hex spelling and stays the canonical key: it is stable
	// and it is what Ethereum-shaped tooling expects. Zenon is the same 20
	// bytes spelled the way syrius spells them, and is what a reader is shown.
	//
	// Sendable says whether a NoM wallet may usefully pay this address. Only a
	// user account is: a contract lives in a namespace no keypair can reach,
	// so ZNN sent there is destroyed rather than delivered. It is carried as a
	// flag rather than by withholding the bech32 spelling, because a UI can
	// act on a flag and cannot act on an absence.
	Address   string `json:"address"`
	Zenon     string `json:"zenon,omitempty"`
	Sendable  bool   `json:"sendable"`
	Namespace string `json:"namespace,omitempty"` // user | embedded | protocol | unreachable
	Kind      string `json:"kind"`                // "account" | "contract"
	Nonce     uint64 `json:"nonce"`
	Balance   string `json:"balance"`
	CodeSize  int    `json:"codeSize"`
	IsToken   bool   `json:"isToken,omitempty"`
	// Inscription is the asset that carries a contract's code, so a reader
	// can go from an address straight to the artifact it commits to.
	Inscription string `json:"inscription,omitempty"`
	// TxCount is how many transactions this account has been party to, set
	// only by the listing (a single account's page shows the transactions
	// themselves, which is a better answer than a count of them).
	TxCount int `json:"txCount,omitempty"`
}

type evmLogJSON struct {
	Address   string   `json:"address"`
	Topics    []string `json:"topics"`
	Data      string   `json:"data"`
	Event     string   `json:"event,omitempty"`
	From      string   `json:"from,omitempty"`
	FromZenon string   `json:"fromZenon,omitempty"`
	To        string   `json:"to,omitempty"`
	ToZenon   string   `json:"toZenon,omitempty"`
	Amount    string   `json:"amount,omitempty"`
	// Decoded names the event's parameters and splits them correctly between
	// topics and data, which the From/To/Amount triple above cannot do for
	// any event that is not shaped like Transfer.
	Decoded *evm.Call `json:"decoded,omitempty"`
}

// deployJSON is what a deployment means, in the absence of an ABI.
//
// A deploy has no selector to look up and no arguments to name: its content is
// init code, and its output is the runtime the constructor returned. So the
// readable facts are different ones — how big the runtime is, which functions
// its dispatcher can reach, and what the constructor was handed.
type deployJSON struct {
	InitCodeSize    int `json:"initCodeSize"`
	RuntimeCodeSize int `json:"runtimeCodeSize"`
	// Interface lists the registered signatures whose selectors appear in the
	// deployed runtime. It is the same heuristic LooksLikeERC20 uses and has
	// the same caveat: a selector in the bytecode means the dispatcher
	// compares against it, not that the function does what its name suggests.
	Interface []string `json:"interface,omitempty"`
	// UnknownSelectors counts four-byte constants in the dispatcher that no
	// registered signature explains, so "3 of 9 functions named" is visible
	// rather than looking like the whole interface.
	UnknownSelectors int `json:"unknownSelectors,omitempty"`
	// ConstructorArgs is the tail of the init code past the compiled
	// contract: solc appends ABI-encoded constructor arguments there, so a
	// whole number of 32-byte words at the end is almost always them. Shown
	// as raw words with every reading, because the constructor's signature is
	// not recoverable from the chain.
	ConstructorArgs []evm.Param `json:"constructorArgs,omitempty"`
	Note            string      `json:"note,omitempty"`
}

type evmTxJSON struct {
	Inscription string `json:"inscription"`
	Op          string `json:"op"`
	From        string `json:"from"`
	FromZenon   string `json:"fromZenon,omitempty"`
	To          string `json:"to"`
	ToZenon     string `json:"toZenon,omitempty"`
	Success     bool   `json:"success"`
	GasUsed     uint64 `json:"gasUsed"`
	GasBudget   uint64 `json:"gasBudget"`
	FeeBurned   string `json:"feeBurned,omitempty"`
	FeeToken    string `json:"feeToken,omitempty"`
	// ResourcePrice is the dynamic-plasma congestion multiplier this call was
	// priced at, in thousandths — 1000 is an uncongested network.
	ResourcePrice uint64 `json:"resourcePrice,omitempty"`

	// Input is the calldata verbatim, and Decoded is what it means. Both are
	// served: the decode depends on a signature registry this explorer
	// supplies, so a reader has to be able to check it against the bytes.
	Input   string      `json:"input,omitempty"`
	Decoded *evm.Call   `json:"decoded,omitempty"`
	Returns []evm.Param `json:"returns,omitempty"`
	// Deploy is the reading of a deployment, which has no calldata and no
	// selector — see describeDeploy.
	Deploy   *deployJSON  `json:"deploy,omitempty"`
	Output   string       `json:"output"`
	Error    string       `json:"error,omitempty"`
	Logs     []evmLogJSON `json:"logs,omitempty"`
	Selector string       `json:"selector,omitempty"`
	Method   string       `json:"method,omitempty"`
	// Height and Timestamp are the carrying momentum — the closest thing to
	// a block this derived chain has. The Blocks page groups by Height.
	Height    uint64 `json:"momentum"`
	Timestamp int64  `json:"timestamp"`
}

// knownSelectors lets the explorer name a call instead of showing four bytes.
// It covers the ERC-20 surface and the demo counter; anything else renders as
// its raw selector, which is honest rather than guessed.
var knownSelectors = func() map[string]string {
	m := map[string]string{}
	for _, sig := range []string{
		evm.ERC20.TotalSupply, evm.ERC20.BalanceOf, evm.ERC20.Transfer,
		evm.ERC20.Approve, evm.ERC20.Allowance, evm.ERC20.TransferFrom,
		evm.ERC20.Decimals, "increment()", "get()", "add(uint256)",
	} {
		m[hex.EncodeToString(evm.Selector(sig))] = sig
	}
	return m
}()

var knownEvents = func() map[string]string {
	m := map[string]string{}
	for _, sig := range []string{evm.ERC20.TransferEvent, evm.ERC20.ApprovalEvent, "Incremented(uint256)"} {
		m[hex.EncodeToString(evm.Topic(sig))] = sig
	}
	return m
}()

func (s *Server) evmAccount(c *evm.Chain, a evm.Address) evmAccountJSON {
	code := c.State.Code(a)
	out := evmAccountJSON{
		Address: a.Hex(), Zenon: zenonOf(a), Sendable: a.Sendable(), Namespace: a.Kind(),
		Kind: "account", Nonce: c.State.Nonce(a),
		Balance: c.State.Balance(a).String(), CodeSize: len(code),
	}
	if len(code) > 0 {
		out.Kind = "contract"
		out.IsToken = evm.LooksLikeERC20(code)
		for _, r := range c.Receipts {
			if r.Op == evm.OpDeploy && r.To == a {
				out.Inscription = r.Inscription
				break
			}
		}
	}
	return out
}

func (s *Server) evmTx(c *evm.Chain, r evm.Receipt) evmTxJSON {
	_ = c
	out := evmTxJSON{
		Inscription: r.Inscription, Op: string(r.Op),
		From: r.From.Hex(), FromZenon: zenonOf(r.From),
		To: r.To.Hex(), ToZenon: zenonOf(r.To),
		Success: r.Success, GasUsed: r.GasUsed,
		GasBudget: r.GasBudget, FeeBurned: r.FeeBurned, FeeToken: r.FeeToken,
		ResourcePrice: r.ResourcePrice,
		Output:        "0x" + hex.EncodeToString(r.Output), Error: r.Err,
		Height: r.Height, Timestamp: r.Timestamp,
	}

	switch r.Op {
	case evm.OpCall:
		out.Input = "0x" + hex.EncodeToString(r.Input)
		if len(r.Input) >= 4 {
			sel := hex.EncodeToString(r.Input[:4])
			out.Selector, out.Method = "0x"+sel, knownSelectors[sel]
			decoded := evm.DecodeKnownCall(r.Input)
			out.Decoded = &decoded
			// Return values only when the call succeeded. A revert's output
			// is an Error(string), not the function's declared return type,
			// and decoding it as the latter produces a confident wrong answer.
			if r.Success {
				out.Returns = evm.DecodeReturn(out.Method, r.Output)
			}
		}
	case evm.OpDeploy:
		out.Deploy = describeDeploy(r)
	}

	for _, lg := range r.Logs {
		l := evmLogJSON{Address: lg.Address.Hex(), Data: "0x" + hex.EncodeToString(lg.Data)}
		for _, t := range lg.Topics {
			l.Topics = append(l.Topics, "0x"+hex.EncodeToString(t))
		}
		decoded := evm.DecodeKnownEvent(lg.Topics, lg.Data)
		l.Decoded = &decoded
		if len(lg.Topics) > 0 {
			if name, ok := knownEvents[hex.EncodeToString(lg.Topics[0])]; ok {
				l.Event = name
				// Transfer and Approval share a shape: two indexed addresses
				// and an amount, which is what makes a token feed renderable.
				// Kept alongside Decoded because the feed views read these
				// three fields directly rather than walking a parameter list.
				if len(lg.Topics) == 3 {
					from, to := evm.DecodeAddress(lg.Topics[1]), evm.DecodeAddress(lg.Topics[2])
					l.From, l.FromZenon = from.Hex(), zenonOf(from)
					l.To, l.ToZenon = to.Hex(), zenonOf(to)
					l.Amount = evm.DecodeWord(lg.Data).String()
				}
			}
		}
		out.Logs = append(out.Logs, l)
	}
	return out
}

// describeDeploy reads a deployment, which has no ABI to decode against.
//
// What it can say is what the artifact turned into: the size of the runtime the
// constructor returned, which registered functions the dispatcher can reach,
// how many it could not name, and the constructor arguments solc appended to
// the init code.
func describeDeploy(r evm.Receipt) *deployJSON {
	d := &deployJSON{InitCodeSize: len(r.Input), RuntimeCodeSize: len(r.Output)}
	if !r.Success {
		d.Note = "the constructor failed, so no runtime code was stored"
		return d
	}

	d.Interface, d.UnknownSelectors = contractInterface(r.Output)

	// Constructor arguments: solc appends them, ABI-encoded, after the init
	// code proper. Their signature is not recoverable, so they are shown as
	// raw words. The tail is only claimed as arguments when it divides evenly
	// into words and is small enough to be an argument list rather than code.
	const maxArgWords = 16
	if tail := trailingWords(r.Input); len(tail) > 0 && len(tail) <= maxArgWords*32 {
		d.ConstructorArgs = evm.DecodeUnknownWords(tail)
		d.Note = "constructor arguments are inferred from the tail of the init code: " +
			"solc appends them there, but nothing on chain says where the code ends"
	}
	return d
}

// contractInterface names the functions a deployed runtime can dispatch to.
//
// A solc dispatcher compares the calldata selector against each of its
// functions with PUSH4, so PUSH4 is what is scanned for rather than every
// four-byte window — the latter finds matches inside unrelated constants and
// reports functions the contract does not have.
//
// It is a heuristic and carries the same caveat LooksLikeERC20 does: a selector
// in the bytecode means the dispatcher compares against it, not that the
// function behaves as its name suggests. That is enough to offer someone a list
// of things to call, and not enough for anything that matters.
func contractInterface(runtime []byte) (sigs []string, unknown int) {
	seen := map[string]bool{}
	for i := 0; i+5 <= len(runtime); i++ {
		if runtime[i] != 0x63 { // PUSH4
			continue
		}
		sel := hex.EncodeToString(runtime[i+1 : i+5])
		if seen[sel] {
			continue
		}
		seen[sel] = true
		if k, ok := evm.KnownSignatures[sel]; ok {
			sigs = append(sigs, k.Sig)
		} else {
			unknown++
		}
	}
	sort.Strings(sigs)
	return sigs, unknown
}

// trailingWords returns the whole 32-byte words at the end of init code that
// follow the compiled contract.
//
// Finding where the contract ends is the hard part, and this does not attempt
// to solve it in general. solc's init code ends with CODECOPY/RETURN followed
// by the runtime and then the metadata CBOR, and the arguments sit past all of
// it — so the tail is found by locating the metadata's own length suffix, which
// solc writes as the last two bytes of the runtime, and taking whole words
// after it. When that is not found, nothing is claimed.
func trailingWords(init []byte) []byte {
	// The CBOR metadata solc appends begins with 0xa2 0x64 "ipfs" or
	// 0xa2 0x64 "bzzr", and ends with a two-byte big-endian length. Search
	// from the end for a length that lands exactly on a marker.
	for i := len(init) - 2; i >= 2; i-- {
		n := int(init[i])<<8 | int(init[i+1])
		start := i - n
		if start < 0 || n < 4 || n > 200 {
			continue
		}
		if init[start] != 0xa2 && init[start] != 0xa3 && init[start] != 0xa1 {
			continue
		}
		rest := init[i+2:]
		if len(rest) == 0 || len(rest)%32 != 0 {
			return nil
		}
		return rest
	}
	return nil
}

// tokenHolder is one address's balance in one token.
type tokenHolder struct {
	Address string `json:"address"`
	Zenon   string `json:"zenon,omitempty"`
	Balance string `json:"balance"`
}

// readToken calls a view function on a token and returns the word it produced.
// A revert reads as zero: a contract that does not answer balanceOf has no
// balance to report, and an explorer that 500s because one contract is odd is
// worse than one that shows a zero.
// readToken runs one view call. v is the request's own copy of the state
// (evm.Chain.View): a page that reads many balances takes one copy, not one
// per balance.
func readToken(v *evm.View, token evm.Address, sig string, args ...any) *big.Int {
	data, err := evm.EncodeCall(sig, args...)
	if err != nil {
		return new(big.Int)
	}
	out, err := v.Query(token, data)
	if err != nil {
		return new(big.Int)
	}
	return evm.DecodeWord(out)
}

// tokenHolders reconstructs a token's holder list from its Transfer events and
// reads each balance back through the contract.
//
// Events are the only route. A Solidity mapping has no enumerable key set, so
// the storage trie cannot be walked to find holders — every explorer rebuilds
// the list from logs, and this is that, not a shortcut around something better.
//
// Shared by the token page and the contract page so the two can never show
// different holders for the same contract.
func tokenHolders(c *evm.Chain, v *evm.View, token evm.Address) []tokenHolder {
	seen := map[evm.Address]bool{}
	topic := hex.EncodeToString(evm.Topic(evm.ERC20.TransferEvent))
	for _, r := range c.Receipts {
		for _, lg := range r.Logs {
			if lg.Address != token || len(lg.Topics) < 3 ||
				hex.EncodeToString(lg.Topics[0]) != topic {
				continue
			}
			seen[evm.DecodeAddress(lg.Topics[1])] = true
			seen[evm.DecodeAddress(lg.Topics[2])] = true
		}
	}
	out := []tokenHolder{}
	for a := range seen {
		if bal := readToken(v, token, evm.ERC20.BalanceOf, a); bal.Sign() > 0 {
			out = append(out, tokenHolder{a.Hex(), zenonOf(a), bal.String()})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		x, _ := new(big.Int).SetString(out[i].Balance, 10)
		y, _ := new(big.Int).SetString(out[j].Balance, 10)
		if x.Cmp(y) != 0 {
			return x.Cmp(y) > 0
		}
		return out[i].Address < out[j].Address
	})
	return out
}

// --- handlers ---

func (s *Server) handleEVMOverview(w http.ResponseWriter, r *http.Request) {
	c, done, err := s.evmRead()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer done()
	// Everything that costs O(state) was computed once when the tail was
	// applied; this handler is polled, and must not scale with history.
	e := &s.evm
	verified := ""
	if !e.verifiedAt.IsZero() {
		verified = e.verifiedAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"chainId":         c.ChainID,
		"stateRoot":       e.root,
		"height":          c.Height,
		"accounts":        c.State.Len(),
		"contracts":       e.contracts,
		"tokens":          e.tokens,
		"txs":             len(c.Receipts),
		"failed":          c.Failed,
		"applied":         c.Applied,
		"skipped":         c.Skipped,
		"gasCap":          c.GasCap,
		"gasPerPlasma":    evm.GasPerPlasma,
		"inscriptionMime": evm.Mime,
		// The dynamic-plasma price the most recent EVM inscription met, in
		// thousandths. 1000 is an uncongested network; above that, gas is
		// proportionally more expensive to buy.
		"resourcePrice": lastResourcePrice(c),
		// contentBytes is the inscribed volume this state was built from.
		// rebuildMs is the last full replay (startup, or a rewritten
		// history) and applyMs the last tail; most requests pay neither.
		// verifiedAt/verifyError report the background full-replay check.
		"contentBytes": e.contentBytes,
		"rebuildMs":    e.rebuildTime.Milliseconds(),
		"applyMs":      e.applyTime.Milliseconds(),
		"rebuilds":     e.rebuilds,
		"verifiedAt":   verified,
		"verifyError":  e.verifyErr,
		// restoredFrom is "genesis" or "checkpoint <height>:<index>" for the
		// last rebuild; checkpointAt is the newest checkpoint this process
		// wrote.
		"restoredFrom": e.restoredFrom,
		"checkpointAt": e.checkpointAt,
	})
}

// lastResourcePrice is the congestion multiplier the newest EVM inscription was
// priced at. It is read off the last receipt rather than from the node, because
// the price a deriver applies is a property of the block it replays; asking the
// node would report the *current* price, which is a different number and would
// disagree with every receipt on the page below it.
//
// An empty chain, or history from before the plasma columns existed, reports the
// floor — which is also what those blocks were charged.
func lastResourcePrice(c *evm.Chain) uint64 {
	for i := len(c.Receipts) - 1; i >= 0; i-- {
		if p := c.Receipts[i].ResourcePrice; p > 0 {
			return p
		}
	}
	return protocol.MinResourcePrice
}

// zenonOf is the NoM spelling of an address — for every address, including
// contracts.
//
// An earlier version returned "" for anything not payable, so that a bech32
// contract address could never be mistaken for a wallet. Measured on the
// devnet, that hid the native spelling of 15 addresses out of 17: it withheld
// the name from exactly the audience the name exists for, and it was
// inconsistent with a protocol that already publishes `z1zen0glyphxdurn…` and
// `z1zen0glyphxescr0w…` for addresses just as unspendable.
//
// The safety fact is carried explicitly instead, as `sendable` on the account
// and as the namespace itself: accounts read `z1q…` and nothing else can, so a
// `z1z…` is visibly not a wallet. A flag a UI can act on is a stronger signal
// than an omission only the initiated would notice.
func zenonOf(a evm.Address) string { return a.Zenon() }

// handleEVMFee quotes what a call must burn to be entitled to `gas`.
//
// It exists so the browser does not have to reimplement the emission schedule.
// The fee is a fraction of a token's supply at an epoch, and that curve is
// consensus for this metaprotocol — a second implementation of it in TypeScript
// would be a second thing to keep in step, and the failure mode is a call that
// underpays and reverts after the plasma is spent.
//
// The quote is only as current as the moment it is taken: the congestion term
// moves per momentum, so a quote taken early underpays if the network gets
// busier, and there are no refunds. Quote late.
func (s *Server) handleEVMFee(w http.ResponseWriter, r *http.Request) {
	c, done, err := s.evmRead()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer done()
	q := r.URL.Query()

	gas, err := strconv.ParseUint(q.Get("gas"), 10, 64)
	if err != nil || gas == 0 {
		writeErr(w, http.StatusBadRequest, "gas must be a positive number")
		return
	}
	if gas > c.GasCap {
		writeErr(w, http.StatusBadRequest,
			"gas exceeds the per-inscription cap of "+strconv.FormatUint(c.GasCap, 10))
		return
	}

	zts := evm.ZnnStandard
	switch strings.ToLower(q.Get("token")) {
	case "", "znn":
	case "qsr":
		zts = evm.QsrStandard
	default:
		writeErr(w, http.StatusBadRequest, `token must be "znn" or "qsr"`)
		return
	}

	// The congestion multiplier the caller expects to meet. A publisher learns
	// it from the same node call that tells it what plasma to pay; absent, the
	// floor is quoted, which is right for an uncongested network and an
	// underestimate for a busy one.
	price := evm.ResourcePrice{}
	if base, err := strconv.ParseUint(q.Get("basePlasma"), 10, 64); err == nil {
		paid, _ := strconv.ParseUint(q.Get("paidPlasma"), 10, 64)
		price = evm.ResourcePrice{Base: base, Paid: paid}
	}

	now := time.Now().Unix()
	fee := c.FeeFor(gas, zts, now, price)
	writeJSON(w, http.StatusOK, map[string]any{
		"gas":           gas,
		"token":         zts,
		"amount":        fee.String(),
		"epoch":         c.Supply.EpochAt(now),
		"resourcePrice": price.Scaled(),
		"burnAddress":   protocol.BurnAddress.String(),
		// Whether a fee is required at all yet, and from where. A caller that
		// burns nothing before this height is not making a mistake.
		"required":     c.FeeFrom > 0,
		"requiredFrom": c.FeeFrom,
	})
}

// page reads limit/offset, clamped. A listing that answers "how many are
// there" as well as "here are some" is what lets a viewer page through
// without guessing whether it has reached the end.
func page(r *http.Request, defLimit, maxLimit int) (limit, offset int) {
	limit, offset = defLimit, 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		offset = v
	}
	return limit, offset
}

// window slices [offset, offset+limit) out of n items, tolerating an offset
// past the end (an empty page, not an error — a caller paging a list that
// shrank under a reorg must get "nothing here" rather than a 500).
func window(n, limit, offset int) (from, to int) {
	if offset > n {
		offset = n
	}
	to = offset + limit
	if to > n {
		to = n
	}
	return offset, to
}

func (s *Server) handleEVMAccounts(w http.ResponseWriter, r *http.Request) {
	c, done, err := s.evmRead()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer done()
	onlyContracts := r.URL.Query().Get("kind") == "contract"

	// How many transactions each account has been party to, counted once for
	// the whole listing rather than per row: a per-account scan of every
	// receipt would be O(accounts x receipts) to print one column.
	txCount := map[evm.Address]int{}
	for _, rec := range c.Receipts {
		txCount[rec.From]++
		if rec.To != rec.From {
			txCount[rec.To]++
		}
	}

	out := []evmAccountJSON{}
	for _, a := range c.State.Accounts() {
		acc := s.evmAccount(c, a)
		if onlyContracts && acc.Kind != "contract" {
			continue
		}
		acc.TxCount = txCount[a]
		out = append(out, acc)
	}
	// Busiest first, then contracts, then by address — so the listing opens on
	// what is actually being used rather than on whichever address sorts
	// lowest, and stays stable across reloads.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TxCount != out[j].TxCount {
			return out[i].TxCount > out[j].TxCount
		}
		if (out[i].Kind == "contract") != (out[j].Kind == "contract") {
			return out[i].Kind == "contract"
		}
		return out[i].Address < out[j].Address
	})

	limit, offset := page(r, 50, 500)
	from, to := window(len(out), limit, offset)
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": out[from:to], "total": len(out),
		"offset": from, "count": to - from,
	})
}

func (s *Server) handleEVMAccount(w http.ResponseWriter, r *http.Request) {
	c, done, err := s.evmRead()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer done()
	a, err := evm.ParseAddress(r.PathValue("addr"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not an address"})
		return
	}
	acc := s.evmAccount(c, a)

	// Storage, sorted. A contract's slots are the only place an explorer can
	// show raw state; for a mapping the keys are hashes and stay hashes,
	// which is why the token view exists separately.
	type slot struct{ Key, Value string }
	slots := []slot{}
	raw := c.State.StorageOf(a)
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		slots = append(slots, slot{"0x" + k, raw[k].String()})
	}

	// The newest handful of transactions, and how many there are in total.
	// The full, paged list lives at /evm/txs?address= — an account page that
	// inlined every transaction would grow without bound for exactly the
	// accounts anyone wants to look at.
	txs := []evmTxJSON{}
	txTotal := 0
	for i := len(c.Receipts) - 1; i >= 0; i-- {
		rec := c.Receipts[i]
		if rec.From != a && rec.To != a {
			continue
		}
		txTotal++
		if len(txs) < 10 {
			txs = append(txs, s.evmTx(c, rec))
		}
	}

	// Every token this account holds, read through each contract. Decimals
	// come from the token itself rather than a UI default, because a token
	// that is not 18-decimal renders off by orders of magnitude otherwise.
	type holding struct {
		Token    string `json:"token"`
		Balance  string `json:"balance"`
		Decimals string `json:"decimals"`
	}
	holdings := []holding{}
	view := c.View()
	for _, other := range c.State.Accounts() {
		if !evm.LooksLikeERC20(c.State.Code(other)) {
			continue
		}
		if bal := readToken(view, other, evm.ERC20.BalanceOf, a); bal.Sign() > 0 {
			holdings = append(holdings, holding{
				other.Hex(), bal.String(),
				readToken(view, other, evm.ERC20.Decimals).String(),
			})
		}
	}

	// When this account IS a token, the holder list belongs here too. A reader
	// who lands on a contract address wants its balances without first
	// discovering that a separate token page exists.
	var tokenInfo map[string]any
	if acc.IsToken {
		tokenInfo = map[string]any{
			"totalSupply": readToken(view, a, evm.ERC20.TotalSupply).String(),
			"decimals":    readToken(view, a, evm.ERC20.Decimals).String(),
			"holders":     tokenHolders(c, view, a),
		}
	}

	// The callable interface, so a page can offer a list of functions rather
	// than a hex field. Computed from the deployed runtime rather than from
	// the deploy receipt, because the runtime is what the address actually
	// dispatches on and is present even for a contract whose deployment this
	// deriver never saw.
	iface, unknownSelectors := contractInterface(c.State.Code(a))

	// Code is served as hex so a contract page can show what the address
	// actually commits to, which is the one thing an explorer can verify
	// without trusting anybody: the address is a commitment to the artifact.
	writeJSON(w, http.StatusOK, map[string]any{
		"account": acc, "storage": slots, "txs": txs, "txTotal": txTotal,
		"holdings": holdings, "token": tokenInfo,
		"interface": iface, "unknownSelectors": unknownSelectors,
		"chainId": c.ChainID,
		"code":    "0x" + hex.EncodeToString(c.State.Code(a)),
	})
}

// handleEVMTxs is the transaction feed, newest first, with the two filters a
// viewer actually navigates by: everything one address was party to, and
// everything one momentum carried.
func (s *Server) handleEVMTxs(w http.ResponseWriter, r *http.Request) {
	c, done, err := s.evmRead()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer done()
	q := r.URL.Query()

	var addr evm.Address
	filterAddr := false
	if v := q.Get("address"); v != "" {
		a, err := evm.ParseAddress(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "not an EVM address: "+v)
			return
		}
		addr, filterAddr = a, true
	}
	height, filterHeight := uint64(0), false
	if v := q.Get("height"); v != "" {
		h, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "height must be a number: "+v)
			return
		}
		height, filterHeight = h, true
	}

	matched := []evm.Receipt{}
	for i := len(c.Receipts) - 1; i >= 0; i-- { // newest first
		rec := c.Receipts[i]
		if filterAddr && rec.From != addr && rec.To != addr {
			continue
		}
		if filterHeight && rec.Height != height {
			continue
		}
		matched = append(matched, rec)
	}

	limit, offset := page(r, 25, 200)
	from, to := window(len(matched), limit, offset)
	out := []evmTxJSON{}
	for _, rec := range matched[from:to] {
		out = append(out, s.evmTx(c, rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"txs": out, "total": len(matched), "offset": from, "count": len(out),
	})
}

// evmBlockJSON is one momentum summarised as a block. This chain has no
// blocks of its own — a momentum is what actually batches EVM inscriptions
// together — so "block" here means "the momentum that carried these".
type evmBlockJSON struct {
	Height    uint64 `json:"height"`
	Timestamp int64  `json:"timestamp"`
	Hash      string `json:"hash,omitempty"`
	Txs       int    `json:"txs"`
	Failed    int    `json:"failed"`
	GasUsed   uint64 `json:"gasUsed"`
}

// evmBlocks folds the receipt list into per-momentum summaries, newest first.
// Receipts are already in canonical (height, index) order, so this is one
// pass with no sort.
func evmBlocks(c *evm.Chain) []evmBlockJSON {
	out := []evmBlockJSON{}
	byHeight := map[uint64]int{} // height -> index into out
	for _, rec := range c.Receipts {
		i, seen := byHeight[rec.Height]
		if !seen {
			out = append(out, evmBlockJSON{Height: rec.Height, Timestamp: rec.Timestamp})
			i = len(out) - 1
			byHeight[rec.Height] = i
		}
		out[i].Txs++
		out[i].GasUsed += rec.GasUsed
		if !rec.Success {
			out[i].Failed++
		}
	}
	// Reverse in place: canonical order is oldest first, a feed reads newest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *Server) handleEVMBlocks(w http.ResponseWriter, r *http.Request) {
	c, done, err := s.evmRead()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer done()
	blocks := evmBlocks(c)
	limit, offset := page(r, 25, 200)
	from, to := window(len(blocks), limit, offset)
	writeJSON(w, http.StatusOK, map[string]any{
		"blocks": blocks[from:to], "total": len(blocks),
		"offset": from, "count": to - from,
	})
}

// handleEVMBlock is one momentum's EVM contents, plus the momentum's own
// identity from the store — the hash is what ties this derived view back to
// something another indexer can check.
//
// Prev and next skip momentums with no EVM activity: stepping through a
// hundred empty momentums to reach the next transaction is not navigation.
func (s *Server) handleEVMBlock(w http.ResponseWriter, r *http.Request) {
	c, done, err := s.evmRead()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer done()
	height, err := strconv.ParseUint(r.PathValue("height"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "height must be a number")
		return
	}

	blocks := evmBlocks(c) // newest first
	var prev, next uint64  // prev = older, next = newer
	found := -1
	for i, b := range blocks {
		if b.Height == height {
			found = i
			break
		}
	}
	if found < 0 {
		writeErr(w, http.StatusNotFound, "no EVM activity in momentum "+strconv.FormatUint(height, 10))
		return
	}
	if found+1 < len(blocks) {
		prev = blocks[found+1].Height
	}
	if found > 0 {
		next = blocks[found-1].Height
	}

	txs := []evmTxJSON{}
	for i := len(c.Receipts) - 1; i >= 0; i-- {
		if c.Receipts[i].Height == height {
			txs = append(txs, s.evmTx(c, c.Receipts[i]))
		}
	}

	block := blocks[found]
	if m, err := s.st.Momentum(height); err == nil && m != nil {
		block.Hash = m.Hash
		block.Timestamp = m.Timestamp
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"block": block, "txs": txs, "prev": prev, "next": next,
	})
}

// evmTransferJSON is one ERC-20 Transfer event, lifted out of the logs and
// given back its transaction's context. Etherscan calls this a token
// transfer; here it is a log line that happens to be the most legible thing
// on the chain, because a raw call to transfer(address,uint256) says nothing
// about whether it succeeded or what it moved.
type evmTransferJSON struct {
	Inscription string `json:"inscription"`
	Height      uint64 `json:"height"`
	Timestamp   int64  `json:"timestamp"`
	Token       string `json:"token"`
	From        string `json:"from"`
	FromZenon   string `json:"fromZenon,omitempty"`
	To          string `json:"to"`
	ToZenon     string `json:"toZenon,omitempty"`
	Amount      string `json:"amount"`
}

// handleEVMTransfers is the token-transfer feed, filterable by token and by
// the address on either side of it.
//
// It reads events rather than calls deliberately: an address that received
// tokens was never the sender of any transaction, so a transaction filter
// cannot find it. Every explorer builds this view from logs for that reason.
func (s *Server) handleEVMTransfers(w http.ResponseWriter, r *http.Request) {
	c, done, err := s.evmRead()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer done()
	q := r.URL.Query()

	var addr, token evm.Address
	filterAddr, filterToken := false, false
	if v := q.Get("address"); v != "" {
		a, err := evm.ParseAddress(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "not an EVM address: "+v)
			return
		}
		addr, filterAddr = a, true
	}
	if v := q.Get("token"); v != "" {
		a, err := evm.ParseAddress(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "not an EVM address: "+v)
			return
		}
		token, filterToken = a, true
	}

	topic := hex.EncodeToString(evm.Topic(evm.ERC20.TransferEvent))
	matched := []evmTransferJSON{}
	for i := len(c.Receipts) - 1; i >= 0; i-- { // newest first
		rec := c.Receipts[i]
		for _, lg := range rec.Logs {
			if len(lg.Topics) < 3 || hex.EncodeToString(lg.Topics[0]) != topic {
				continue
			}
			if filterToken && lg.Address != token {
				continue
			}
			from, to := evm.DecodeAddress(lg.Topics[1]), evm.DecodeAddress(lg.Topics[2])
			if filterAddr && from != addr && to != addr {
				continue
			}
			matched = append(matched, evmTransferJSON{
				Inscription: rec.Inscription, Height: rec.Height, Timestamp: rec.Timestamp,
				Token: lg.Address.Hex(),
				From:  from.Hex(), FromZenon: zenonOf(from),
				To: to.Hex(), ToZenon: zenonOf(to),
				Amount: evm.DecodeWord(lg.Data).String(),
			})
		}
	}

	limit, offset := page(r, 25, 200)
	from, to := window(len(matched), limit, offset)
	writeJSON(w, http.StatusOK, map[string]any{
		"transfers": matched[from:to], "total": len(matched),
		"offset": from, "count": to - from,
	})
}

func (s *Server) handleEVMTx(w http.ResponseWriter, r *http.Request) {
	c, done, err := s.evmRead()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer done()
	// Indexed, not scanned: this is what every client polls while it waits
	// for a call to land.
	rec, ok := c.Receipt(strings.ToLower(r.PathValue("id")))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such EVM transaction"})
		return
	}
	writeJSON(w, http.StatusOK, s.evmTx(c, rec))
}

func (s *Server) handleEVMToken(w http.ResponseWriter, r *http.Request) {
	c, done, err := s.evmRead()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer done()
	token, err := evm.ParseAddress(r.PathValue("addr"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not an address"})
		return
	}
	// The transfer feed, newest first. Holders come from the shared scan so
	// this page and the contract page cannot disagree about them.
	transfers := []evmLogJSON{}
	topic := hex.EncodeToString(evm.Topic(evm.ERC20.TransferEvent))
	for i := len(c.Receipts) - 1; i >= 0; i-- {
		for _, lg := range c.Receipts[i].Logs {
			if lg.Address != token || len(lg.Topics) < 3 ||
				hex.EncodeToString(lg.Topics[0]) != topic {
				continue
			}
			transfers = append(transfers, evmLogJSON{
				Address: token.Hex(), Event: evm.ERC20.TransferEvent,
				From:   evm.DecodeAddress(lg.Topics[1]).Hex(),
				To:     evm.DecodeAddress(lg.Topics[2]).Hex(),
				Amount: evm.DecodeWord(lg.Data).String(),
			})
		}
	}
	view := c.View()
	holders := tokenHolders(c, view, token)

	writeJSON(w, http.StatusOK, map[string]any{
		"address":     token.Hex(),
		"isToken":     evm.LooksLikeERC20(c.State.Code(token)),
		"totalSupply": readToken(view, token, evm.ERC20.TotalSupply).String(),
		"decimals":    readToken(view, token, evm.ERC20.Decimals).String(),
		"holders":     holders,
		"transfers":   transfers,
	})
}
