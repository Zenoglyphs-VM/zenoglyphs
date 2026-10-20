package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/evm"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/inscribe"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// The EVM front end.
//
// Deploy and call publish ordinary inscriptions; state and receipts derive.
// Nothing here writes EVM state to the chain, because there is none to write:
// the contracts and the calls are the permanent artifacts, and the world state
// is a projection of them in canonical order — the same relationship the
// Zenoglyphs indexer already has with its own event log.
func cmdEVM(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: zcribe evm <deploy|call|simulate|swarm|query|token|state|receipts|address> [flags]")
	}
	switch args[0] {
	case "deploy":
		return evmDeploy(ctx, args[1:])
	case "call":
		return evmCall(ctx, args[1:])
	case "simulate":
		return evmSimulate(ctx, args[1:])
	case "swarm":
		return evmSwarm(ctx, args[1:])
	case "state":
		return evmState(args[1:])
	case "receipts":
		return evmReceipts(args[1:])
	case "query":
		return evmQuery(args[1:])
	case "token":
		return evmToken(args[1:])
	case "address":
		return evmAddress(args[1:])
	default:
		return fmt.Errorf("unknown evm subcommand %q", args[0])
	}
}

// readCode accepts either a raw binary file or a hex text file, because both
// are what compilers and hand-assemblers actually emit.
func readCode(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	trimmed = strings.TrimPrefix(trimmed, "0x")
	if b, err := hex.DecodeString(trimmed); err == nil && len(b) > 0 {
		return b, nil
	}
	return raw, nil
}

func evmDeploy(ctx context.Context, args []string) error {
	set := fs("evm deploy")
	var w walletFlags
	w.register(set)
	file := set.String("file", "", "init bytecode, raw or hex (required)")
	name := set.String("name", "", "asset name")
	var f feeFlags
	f.register(set, defaultDeployGas)

	_ = set.Parse(args)
	if *file == "" {
		return fmt.Errorf("-file is required")
	}
	code, err := readCode(*file)
	if err != nil {
		return err
	}
	in, err := w.inscriber(ctx)
	if err != nil {
		return err
	}
	nm := *name
	if nm == "" {
		nm = "contract.evm"
	}
	meta, err := evmMeta(nm, evm.OpDeploy, "", &f, in.ChainID())
	if err != nil {
		return err
	}
	pay, note, err := payment(ctx, in, &f, meta, code)
	if err != nil {
		return err
	}
	hash, err := in.InscribeFilePaying(ctx, meta, code, protocol.PlasmaFused, in.Address(), pay)
	if err != nil {
		return err
	}
	if note != "" {
		fmt.Printf("burned %s\n", note)
	}
	fmt.Printf("deployed %d bytes of init code\n", len(code))
	fmt.Printf("  asset id: %s\n", hash)
	deployed := evm.ContractAddress(hash.Bytes())
	fmt.Printf("  contract: %s\n", deployed.Hex())
	fmt.Printf("  zenon:    %s\n", deployed.Zenon())
	fmt.Printf("\nThe contract address is 0x16 ‖ keccak(assetId)[13:] — a commitment to\n")
	fmt.Printf("the artifact, so it names its own code and cannot be redeployed\n")
	fmt.Printf("elsewhere. The leading byte puts it outside every namespace a keypair\n")
	fmt.Printf("can reach, so it can never collide with an account — and for the same\n")
	fmt.Printf("reason it is not an address to send ZNN to.\n")
	return nil
}

func evmCall(ctx context.Context, args []string) error {
	set := fs("evm call")
	var w walletFlags
	w.register(set)
	to := set.String("to", "", "contract address, 0x… or z1… (required)")
	data := set.String("data", "", "calldata as hex, e.g. the 4-byte selector plus arguments")
	file := set.String("data-file", "", "calldata from a file instead of -data")
	sig := set.String("sig", "", `function signature, e.g. "transfer(address,uint256)"`)
	argv := set.String("args", "", "comma-separated arguments for -sig: addresses (0x… or z1…) or decimal numbers")
	var f feeFlags
	f.register(set, defaultCallGas)
	_ = set.Parse(args)
	if *to == "" {
		return fmt.Errorf("-to is required")
	}
	addr, err := evm.ParseAddress(*to)
	if err != nil {
		return err
	}
	var calldata []byte
	switch {
	case *sig != "":
		if calldata, err = encodeCall(*sig, *argv); err != nil {
			return err
		}
	case *file != "":
		if calldata, err = readCode(*file); err != nil {
			return err
		}
	case *data != "":
		if calldata, err = hex.DecodeString(strings.TrimPrefix(*data, "0x")); err != nil {
			return fmt.Errorf("-data must be hex: %w", err)
		}
	}
	in, err := w.inscriber(ctx)
	if err != nil {
		return err
	}
	meta, err := evmMeta("call.evm", evm.OpCall, addr.Hex(), &f, in.ChainID())
	if err != nil {
		return err
	}
	pay, note, err := payment(ctx, in, &f, meta, calldata)
	if err != nil {
		return err
	}
	hash, err := in.InscribeFilePaying(ctx, meta, calldata, protocol.PlasmaFused, in.Address(), pay)
	if err != nil {
		return err
	}
	fmt.Printf("call published\n  asset id: %s\n  to:       %s\n  zenon:    %s\n  calldata: %s\n",
		hash, addr.Hex(), addr.Zenon(), hex.EncodeToString(calldata))
	if note != "" {
		fmt.Printf("  burned:   %s\n", note)
	}
	fmt.Printf("\nRun 'zcribe evm state' once the indexer has caught up.\n")
	return nil
}

// derive is the whole EVM: read every application/evm inscription and replay.
func derive(dbPath string, chainID uint64) (*evm.Chain, error) {
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	assets, err := st.AssetsOfMimeCarried(evm.Mime)
	if err != nil {
		return nil, err
	}
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
	c := evm.NewChain(chainID)
	if err := c.Apply(ins); err != nil {
		return nil, err
	}
	return c, nil
}

func evmState(args []string) error {
	set := fs("evm state")
	db := set.String("db", "zenoglyphs.db", "SQLite database path")
	chain := set.Uint64("chain", 69, "chain identifier reported to CHAINID")
	addr := set.String("addr", "", "show only this account")
	_ = set.Parse(args)

	c, err := derive(*db, *chain)
	if err != nil {
		return err
	}
	fmt.Printf("derived from %d EVM inscriptions (%d applied, %d skipped)\n",
		c.Applied+c.Skipped, c.Applied, c.Skipped)
	fmt.Printf("state root: %s\n\n", c.State.Root())

	accounts := c.State.Accounts()
	if *addr != "" {
		want, err := evm.ParseAddress(*addr)
		if err != nil {
			return err
		}
		accounts = []evm.Address{want}
	}
	for _, a := range accounts {
		code := c.State.Code(a)
		kind := "account"
		if len(code) > 0 {
			kind = fmt.Sprintf("contract, %d bytes of code", len(code))
		}
		fmt.Printf("%s  %s\n", a.Hex(), kind)
		fmt.Printf("  nonce %d  balance %s\n", c.State.Nonce(a), c.State.Balance(a))
		slots := c.State.StorageOf(a)
		if len(slots) == 0 {
			continue
		}
		keys := make([]string, 0, len(slots))
		for k := range slots {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  slot 0x%s = %s\n", strings.TrimLeft(k, "0")+zeroIfEmpty(k), slots[k])
		}
	}
	return nil
}

func zeroIfEmpty(k string) string {
	if strings.Trim(k, "0") == "" {
		return "0"
	}
	return ""
}

func evmReceipts(args []string) error {
	set := fs("evm receipts")
	db := set.String("db", "zenoglyphs.db", "SQLite database path")
	chain := set.Uint64("chain", 69, "chain identifier reported to CHAINID")
	_ = set.Parse(args)

	c, err := derive(*db, *chain)
	if err != nil {
		return err
	}
	if len(c.Receipts) == 0 {
		fmt.Println("no EVM inscriptions found")
		return nil
	}
	fmt.Printf("%-8s %-12s %-44s %-8s %-9s %s\n", "OP", "INSCRIPTION", "CONTRACT", "OK", "GAS", "RESULT")
	for _, r := range c.Receipts {
		ok := "yes"
		result := "0x" + hex.EncodeToString(r.Output)
		if !r.Success {
			ok, result = "no", r.Err
		} else if len(r.Output) == 32 {
			result = fmt.Sprintf("%s (0x%s)", new(big.Int).SetBytes(r.Output),
				strings.TrimLeft(hex.EncodeToString(r.Output), "0"))
		} else if len(r.Output) == 0 {
			result = "-"
		}
		fmt.Printf("%-8s %-12s %-44s %-8s %-9d %s\n",
			r.Op, r.Inscription[:10], r.To.Hex(), ok, r.GasUsed, result)
		for _, lg := range r.Logs {
			fmt.Printf("%-8s %-12s   log topic0=%s data=%s\n", "", "",
				hex.EncodeToString(lg.Topics[0])[:16]+"…", hex.EncodeToString(lg.Data))
		}
	}
	return nil
}

// evmAddress converts between the two spellings of one address.
//
// It exists because the two audiences hold different halves: a syrius user
// knows their `z1q…` and has no way to produce the hex, and someone reading
// Ethereum-shaped tooling has the hex and cannot recognise whose account it is.
// Both are the same 20 bytes, so this prints both and names the namespace.
func evmAddress(args []string) error {
	set := fs("evm address")
	addr := set.String("addr", "", "an address in either spelling, shown as both")
	nom := set.String("nom", "", "alias for -addr")
	asset := set.String("asset", "", "an asset id whose contract address to derive")
	_ = set.Parse(args)

	if *addr == "" {
		*addr = *nom
	}
	if *addr != "" {
		a, err := evm.ParseAddress(*addr)
		if err != nil {
			return err
		}
		fmt.Printf("%s\n  hex:   %s\n  zenon: %s\n  kind:  %s\n",
			*addr, a.Hex(), a.Zenon(), a.Kind())
		if !a.Sendable() {
			fmt.Printf("\n  Not a payable address: byte 0 is %#02x, which no keypair can\n"+
				"  reach, so ZNN sent here from a wallet is destroyed rather than\n"+
				"  delivered. It is a contract handle, not an account.\n", a[0])
		}
	}
	if *asset != "" {
		b, err := hex.DecodeString(*asset)
		if err != nil {
			return err
		}
		a := evm.ContractAddress(b)
		fmt.Printf("%s\n  contract: %s\n  zenon:    %s\n", *asset, a.Hex(), a.Zenon())
	}
	if *addr == "" && *asset == "" {
		return fmt.Errorf("pass -addr or -asset")
	}
	return nil
}

// encodeCall builds calldata from a human-typed signature and argument list.
//
// Arguments are parsed by shape rather than by the signature's declared types:
// anything that reads as an address in either spelling is one — 0x-prefixed
// hex, or the z1… a holder copies out of syrius — and everything else is a
// decimal integer. That is enough for the static-word ABI this prototype
// speaks, and it fails loudly rather than silently mis-encoding.
//
// Taking the native spelling here is the difference between
//
//	-args z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d,100
//
// and asking a holder to convert their own address to hex first, which is a
// step they have no tool for and would get wrong by hand.
func encodeCall(sig, argv string) ([]byte, error) {
	var args []any
	for _, raw := range strings.Split(argv, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "0x") && len(raw) == 42 ||
			strings.HasPrefix(raw, "z1") {
			a, err := evm.ParseAddress(raw)
			if err != nil {
				return nil, err
			}
			args = append(args, a)
			continue
		}
		n, ok := new(big.Int).SetString(raw, 10)
		if !ok {
			return nil, fmt.Errorf("cannot read %q as an address (0x… or z1…) or a decimal number", raw)
		}
		args = append(args, n)
	}
	return evm.EncodeCall(sig, args...)
}

// evmQuery reads through a contract without publishing anything.
func evmQuery(args []string) error {
	set := fs("evm query")
	db := set.String("db", "zenoglyphs.db", "SQLite database path")
	chain := set.Uint64("chain", 69, "chain identifier reported to CHAINID")
	to := set.String("to", "", "contract address (required)")
	sig := set.String("sig", "", `function signature, e.g. "balanceOf(address)"`)
	argv := set.String("args", "", "comma-separated arguments")
	data := set.String("data", "", "raw calldata hex, instead of -sig/-args")
	_ = set.Parse(args)
	if *to == "" {
		return fmt.Errorf("-to is required")
	}
	addr, err := evm.ParseAddress(*to)
	if err != nil {
		return err
	}
	var calldata []byte
	if *sig != "" {
		if calldata, err = encodeCall(*sig, *argv); err != nil {
			return err
		}
	} else if calldata, err = hex.DecodeString(strings.TrimPrefix(*data, "0x")); err != nil {
		return fmt.Errorf("-data must be hex: %w", err)
	}
	c, err := derive(*db, *chain)
	if err != nil {
		return err
	}
	out, err := c.Query(addr, calldata)
	if err != nil {
		return fmt.Errorf("call reverted: %w", err)
	}
	fmt.Printf("0x%s\n", hex.EncodeToString(out))
	if len(out) == 32 {
		fmt.Printf("  as uint256: %s\n", evm.DecodeWord(out))
		fmt.Printf("  as address: %s\n", evm.DecodeAddress(out).Hex())
	}
	return nil
}

// evmToken renders an ERC-20 the way a holder wants to see it.
func evmToken(args []string) error {
	set := fs("evm token")
	db := set.String("db", "zenoglyphs.db", "SQLite database path")
	chain := set.Uint64("chain", 69, "chain identifier reported to CHAINID")
	at := set.String("addr", "", "token contract address (required)")
	_ = set.Parse(args)
	if *at == "" {
		return fmt.Errorf("-addr is required")
	}
	token, err := evm.ParseAddress(*at)
	if err != nil {
		return err
	}
	c, err := derive(*db, *chain)
	if err != nil {
		return err
	}
	if !evm.LooksLikeERC20(c.State.Code(token)) {
		fmt.Printf("warning: %s does not carry the ERC-20 selectors\n\n", token.Hex())
	}
	read := func(sig string, a ...any) *big.Int {
		data, err := evm.EncodeCall(sig, a...)
		if err != nil {
			return new(big.Int)
		}
		out, err := c.Query(token, data)
		if err != nil {
			return new(big.Int)
		}
		return evm.DecodeWord(out)
	}
	dec := read(evm.ERC20.Decimals)
	fmt.Printf("token %s\n  totalSupply %s  decimals %s\n\n",
		token.Hex(), read(evm.ERC20.TotalSupply), dec)

	// Holders are found from Transfer logs rather than by walking storage:
	// a mapping has no enumerable key set, which is why every explorer
	// reconstructs holders from events.
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
	if len(seen) == 0 {
		fmt.Println("no Transfer events yet")
		return nil
	}
	holders := make([]evm.Address, 0, len(seen))
	for a := range seen {
		holders = append(holders, a)
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].Hex() < holders[j].Hex() })
	fmt.Printf("%-44s %s\n", "HOLDER", "BALANCE")
	for _, h := range holders {
		fmt.Printf("%-44s %s\n", h.Hex(), read(evm.ERC20.BalanceOf, h))
	}
	return nil
}

// --- paying for gas ---

// Default gas, and therefore default spend, when the caller does not say.
//
// Sized from measurements rather than guessed: a stock solc ERC-20 deploys in
// 374,152 gas and transfers in 13,244 (internal/evm/solc_test.go). The
// headroom is deliberate — the fee *is* the gas limit, there are no refunds,
// and a call that runs out has burned the ZNN anyway. Overpaying a fraction of
// a cent beats re-publishing.
const (
	defaultDeployGas = 600_000
	defaultCallGas   = 100_000
)

// feeFlags are the two knobs a publisher has over the burned fee.
//
// The fee *is* the gas limit: there are no refunds, nothing in derived state
// can send ZNN back, so choosing what to burn is choosing how much deriver time
// the call may use. That makes the honest interface "how much gas do you want",
// with the cost quoted from it — not "how much do you want to spend".
type feeFlags struct {
	gas   *uint64
	token *string
}

func (f *feeFlags) register(set *flag.FlagSet, defaultGas uint64) {
	f.gas = set.Uint64("gas", defaultGas,
		"gas this inscription may use, and what the burned fee is quoted for")
	f.token = set.String("fee-token", "znn", "burn znn or qsr to pay for gas")
}

func (f *feeFlags) standard() (string, types.ZenonTokenStandard, error) {
	switch strings.ToLower(*f.token) {
	case "znn":
		return evm.ZnnStandard, types.ZnnTokenStandard, nil
	case "qsr":
		return evm.QsrStandard, types.QsrTokenStandard, nil
	default:
		return "", types.ZeroTokenStandard, fmt.Errorf("-fee-token must be znn or qsr, got %q", *f.token)
	}
}

// evmMeta builds the mint envelope for an EVM inscription.
//
// The declared gas ceiling and the fee are quoted from the same number on
// purpose. An inscription may lower its own budget but never raise it, so
// declaring 100,000 while burning enough for 600,000 would destroy value for
// gas the deriver will not grant — the two have to agree or the caller is
// paying for nothing.
func evmMeta(name string, op evm.Op, to string, f *feeFlags, chainID uint64) (protocol.MintWire, error) {
	ext := map[string]any{"op": string(op)}
	if to != "" {
		ext["to"] = to
	}
	// The ceiling is only declared where it is also paid for. On a chain with
	// no fee rule the budget comes from plasma alone, and pinning it here
	// would shrink it for no benefit.
	if evm.FeeFromHeight[chainID] != 0 && *f.gas > 0 {
		ext["gas"] = *f.gas
	}
	extBytes, err := protocol.Marshal(map[string]any{"evm": ext})
	if err != nil {
		return protocol.MintWire{}, err
	}
	return protocol.MintWire{Name: name, Mime: evm.Mime, Supply: 1, Ext: extBytes}, nil
}

// payment quotes the fee for the requested gas and builds the carrier payment,
// or returns nil when this chain has no fee rule yet.
//
// The quote is taken here, immediately before publishing, and against the
// exact encoded block size, because both terms of the price move: the supply
// term with the epoch and the congestion term with the momentum. A fee
// computed from stale numbers buys less gas than it was meant to, and the ZNN
// is destroyed either way.
func payment(ctx context.Context, in *inscribe.Inscriber, f *feeFlags, meta protocol.MintWire, content []byte) (*inscribe.Payment, string, error) {
	from := evm.FeeFromHeight[in.ChainID()]
	if from == 0 {
		return nil, "", nil // no fee rule on this chain
	}
	if *f.gas == 0 {
		return nil, "", fmt.Errorf("-gas must be greater than zero: this chain requires a burned fee from momentum %d, and the fee is quoted from the gas", from)
	}

	zts, token, err := f.standard()
	if err != nil {
		return nil, "", err
	}

	q, err := in.Quote(ctx, meta, content, protocol.PlasmaFused)
	if err != nil {
		return nil, "", fmt.Errorf("quote: %w", err)
	}
	price := evm.ResourcePrice{Base: q.BasePlasma, Paid: q.PaidPlasma}

	c := evm.NewChain(in.ChainID())
	fee := c.FeeFor(*f.gas, zts, time.Now().Unix(), price)
	if fee.Sign() <= 0 {
		return nil, "", fmt.Errorf("quoted a fee of %s for %d gas, which cannot be right", fee, *f.gas)
	}

	note := fmt.Sprintf("%s raw %s for %s gas (resource price %.3fx; required from momentum %d)",
		fee, strings.ToUpper(*f.token), strconv.FormatUint(*f.gas, 10),
		float64(price.Scaled())/float64(protocol.PriceScaleFactor), from)
	return &inscribe.Payment{To: protocol.BurnAddress, Amount: fee, Token: token}, note, nil
}
