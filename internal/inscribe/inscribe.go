// Package inscribe builds, signs, and publishes the account blocks that carry
// inscriptions.
//
// It reuses go-zenon's own primitives — block hashing, PoW, key derivation —
// rather than reimplementing them. A reimplementation that drifts by one byte
// produces blocks the network silently refuses, and the failure surfaces as
// "nothing happened" rather than as an error.
package inscribe

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/pow"
	"github.com/zenon-network/go-zenon/wallet"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
)

const (
	blockVersion         = 1
	blockTypeUserSend    = 2
	blockTypeUserReceive = 3
)

type Inscriber struct {
	client *rpcclient.Client
	key    *wallet.KeyPair
	addr   types.Address
	chain  uint64
	log    *slog.Logger

	// ConfirmWait is how long to wait for a submitted block to be confirmed
	// before giving up. Multi-part chains need each block confirmed before
	// the next can reference its hash.
	ConfirmWait time.Duration

	// HeadroomPercent is fused plasma paid over the network price, in percent.
	//
	// A block commits to the price it was signed at, and dp.ValidPrice
	// rejects it in any momentum whose price is higher. Paying the quote
	// exactly therefore makes a block valid for one momentum only: if the
	// momentum is full and it is left out, the price rises (by up to ~10% per
	// momentum under load) and the block sits pending until the price decays
	// back — freezing every later block on the account (research/
	// evm-benchmark.md, "Wedged accounts"). Headroom keeps it valid through a
	// short climb. It is not free for an EVM call: the deriver reads the price
	// off what the block paid, so the fee is quoted — and burned — at the
	// padded price too. Zero pays the quote exactly.
	HeadroomPercent uint64
}

func New(client *rpcclient.Client, key *wallet.KeyPair, chainID uint64, log *slog.Logger) (*Inscriber, error) {
	addr := types.PubKeyToAddress(key.Public)
	return &Inscriber{
		client: client, key: key, addr: addr, chain: chainID, log: log,
		ConfirmWait: 2 * time.Minute,
	}, nil
}

func (in *Inscriber) Address() types.Address { return in.addr }

// Send builds, signs, and publishes one account block, returning its hash.
func (in *Inscriber) Send(ctx context.Context, to types.Address, amount *big.Int, zts types.ZenonTokenStandard, data []byte) (types.Hash, error) {
	if len(data) > protocol.MaxDataLength {
		return types.ZeroHash, fmt.Errorf("data %d bytes exceeds the %d byte block limit",
			len(data), protocol.MaxDataLength)
	}
	if amount == nil {
		amount = big.NewInt(0)
	}

	// Height and previousHash come from this account's own frontier. An
	// account that has never sent starts at height 1 with a zero previous
	// hash.
	height := uint64(1)
	previous := types.ZeroHash
	if frontier, err := in.client.FrontierAccountBlock(ctx, in.addr); err == nil && frontier != nil {
		height = frontier.Height + 1
		previous = frontier.Hash
	}

	momentum, err := in.client.FrontierMomentum(ctx)
	if err != nil {
		return types.ZeroHash, fmt.Errorf("frontier momentum: %w", err)
	}

	block := &nom.AccountBlock{
		Version:              blockVersion,
		ChainIdentifier:      in.chain,
		BlockType:            blockTypeUserSend,
		PreviousHash:         previous,
		Height:               height,
		MomentumAcknowledged: types.HashHeight{Hash: momentum.Hash, Height: momentum.Height},
		Address:              in.addr,
		ToAddress:            to,
		Amount:               amount,
		TokenStandard:        zts,
		FromBlockHash:        types.ZeroHash,
		Data:                 data,
		PublicKey:            in.key.Public,
	}

	if err := in.payPlasma(ctx, block, momentum.NextFusionPrice); err != nil {
		return types.ZeroHash, err
	}

	block.Hash = block.ComputeHash()
	block.Signature = in.key.Sign(block.Hash.Bytes())

	if err := in.client.PublishRawTransaction(ctx, block); err != nil {
		return types.ZeroHash, fmt.Errorf("publish: %w", err)
	}
	in.log.Debug("published block", "hash", block.Hash, "height", height, "data", len(data))
	return block.Hash, nil
}

// Receive takes delivery of one send addressed to this account.
//
// Until this runs the value is not in the account's balance: a send on NoM is
// final for the sender the moment it confirms, and lands for the recipient
// only when the recipient publishes a block naming it. Not receiving is
// therefore a real choice — a payment nobody can attribute can be left where
// it is rather than absorbed — which is how the Telegram bot's credit ledger
// avoids taking money it cannot credit to anyone.
//
// A receive carries no value fields of its own: the amount, token and sender
// all come from the block it names, so filling them in here would either be
// ignored or rejected.
func (in *Inscriber) Receive(ctx context.Context, from types.Hash) (types.Hash, error) {
	height := uint64(1)
	previous := types.ZeroHash
	if frontier, err := in.client.FrontierAccountBlock(ctx, in.addr); err == nil && frontier != nil {
		height = frontier.Height + 1
		previous = frontier.Hash
	}
	momentum, err := in.client.FrontierMomentum(ctx)
	if err != nil {
		return types.ZeroHash, fmt.Errorf("frontier momentum: %w", err)
	}

	block := &nom.AccountBlock{
		Version:              blockVersion,
		ChainIdentifier:      in.chain,
		BlockType:            blockTypeUserReceive,
		PreviousHash:         previous,
		Height:               height,
		MomentumAcknowledged: types.HashHeight{Hash: momentum.Hash, Height: momentum.Height},
		Address:              in.addr,
		ToAddress:            types.ZeroAddress,
		Amount:               big.NewInt(0),
		TokenStandard:        types.ZeroTokenStandard,
		FromBlockHash:        from,
		Data:                 nil,
		PublicKey:            in.key.Public,
	}
	if err := in.payPlasma(ctx, block, momentum.NextFusionPrice); err != nil {
		return types.ZeroHash, err
	}
	block.Hash = block.ComputeHash()
	block.Signature = in.key.Sign(block.Hash.Bytes())
	if err := in.client.PublishRawTransaction(ctx, block); err != nil {
		return types.ZeroHash, fmt.Errorf("publish receive: %w", err)
	}
	in.log.Debug("received block", "from", from, "hash", block.Hash, "height", height)
	return block.Hash, nil
}

// payPlasma fills in fusedPlasma, difficulty, and nonce.
//
// The split between fused and PoW is the node's call, not ours: under dynamic
// plasma the price moves per momentum, so asking is the only way to be right.
// A 16 KB block needs roughly 541 QSR fused; PoW alone tops out at 1,080
// bytes of data, which is why large inscriptions require a fused account.
//
// fusionPrice is the resource price the frontier momentum publishes for the
// next one, and paying it is not optional. `basePlasma` is *nominal* under
// dynamic plasma — what a block must actually put up is
//
//	ceil(basePlasma x fusionPrice / PriceScaleFactor)
//
// and go-zenon's dp.ValidPrice compares against exactly that. This function
// set FusedPlasma to the nominal figure until 2026-09-10, which is correct at
// the floor price and rejected at every price above it: the bug was invisible
// on an idle devnet and would have broken every publish the moment the network
// got busy. A zero price is a pre-spork node, where nominal is the whole story.
func (in *Inscriber) payPlasma(ctx context.Context, block *nom.AccountBlock, fusionPrice uint64) error {
	req, err := in.client.RequiredPoW(ctx, block.Address, block.ToAddress, block.BlockType, block.Data)
	if err != nil {
		return fmt.Errorf("required plasma: %w", err)
	}

	if req.RequiredDifficulty == 0 {
		required, err := requiredFusedPlasma(req.BasePlasma, fusionPrice)
		if err != nil {
			return err
		}
		fused := in.withHeadroom(required)
		if fused > req.AvailablePlasma && req.AvailablePlasma >= required {
			// Headroom is a margin, not a requirement: an account that can
			// cover the block but not the margin pays what it has.
			fused = req.AvailablePlasma
		}
		if fused > req.AvailablePlasma && req.AvailablePlasma > 0 {
			// The node said no proof of work was needed, so it believes this
			// account can cover the block; disagreeing here means the price
			// moved between its answer and ours. Say so rather than publish a
			// block that will be refused.
			in.log.Warn("resource price moved since the node quoted this block",
				"required", fused, "available", req.AvailablePlasma, "price", fusionPrice)
		}
		block.FusedPlasma = fused
		block.Difficulty = 0
		block.Nonce = nom.Nonce{}
		return nil
	}

	if req.RequiredDifficulty > 0 && len(block.Data) > protocol.MaxDataLengthPoW {
		in.log.Warn("block needs proof of work but exceeds the PoW data ceiling",
			"bytes", len(block.Data), "pow_max", protocol.MaxDataLengthPoW,
			"difficulty", req.RequiredDifficulty)
	}

	block.FusedPlasma = req.AvailablePlasma
	block.Difficulty = req.RequiredDifficulty

	// The PoW preimage is address ‖ previousHash — deliberately not the block
	// hash, so the nonce can be found before the rest of the block is final.
	start := time.Now()
	nonce := pow.GetPoWNonce(new(big.Int).SetUint64(req.RequiredDifficulty), pow.GetAccountBlockHash(block))
	copy(block.Nonce.Data[:], nonce)
	in.log.Debug("proof of work done", "difficulty", req.RequiredDifficulty, "took", time.Since(start))
	return nil
}

// WaitForConfirmation blocks until the node reports the block inside a
// momentum. Multi-part chains need this: a part's hash has to exist before
// the next part can name it.
func (in *Inscriber) WaitForConfirmation(ctx context.Context, hash types.Hash) (uint64, error) {
	deadline := time.Now().Add(in.ConfirmWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Second):
		}
		b, err := in.client.AccountBlockByHash(ctx, hash)
		if err != nil {
			continue
		}
		if b != nil && b.ConfirmationDetail != nil && b.ConfirmationDetail.MomentumHeight > 0 {
			return b.ConfirmationDetail.MomentumHeight, nil
		}
	}
	return 0, fmt.Errorf("block %s not confirmed within %s", hash, in.ConfirmWait)
}

// Block is the node's view of one account block, including what it paid in
// plasma and work.
func (in *Inscriber) Block(ctx context.Context, hash types.Hash) (*rpcclient.AccountBlock, error) {
	return in.client.AccountBlockByHash(ctx, hash)
}

// InscribeFile publishes a complete mint, splitting across blocks if needed.
//
// The chain is built backwards — tail first, head last — because every part
// references the hash of a block that must already exist. That ordering also
// satisfies the protocol's "every part precedes the head" rule for free.
// Payment is value the *carrier block itself* moves, as distinct from the
// inscription it carries.
//
// One metaprotocol needs this: the EVM prices its own execution in burned ZNN
// or QSR, and proves the burn by the block's recipient being the protocol sink
// (internal/evm/fee.go). Nothing in Zenoglyphs itself reads a carrier amount —
// R-7.2 says a transfer must ignore it — so this is deliberately not something
// an ordinary mint sets.
//
// It rides the head block, and only the head: a multi-part chain has one asset
// id, the head's, and that is the block a deriver joins the payment to.
type Payment struct {
	// To is the block's recipient. For an EVM fee it is protocol.BurnAddress,
	// which is unspendable by construction (A-26) — that is what makes the
	// burn provable rather than promised.
	//
	// It is also who a mint issues its supply to (internal/indexer/apply.go
	// sets Owner from it), so paying a fee this way issues the carrier asset
	// to the sink as well. For an EVM inscription that is the right reading:
	// the asset is a receipt for a call, not a collectible, and burning it
	// alongside the fee is more honest than parking it in the caller's
	// gallery.
	To     types.Address
	Amount *big.Int
	Token  types.ZenonTokenStandard
}

func (in *Inscriber) InscribeFile(ctx context.Context, meta protocol.MintWire, content []byte, mode protocol.PlasmaMode, to types.Address) (types.Hash, error) {
	return in.InscribeFilePaying(ctx, meta, content, mode, to, nil)
}

// InscribeFilePaying is InscribeFile with value attached to the head block.
// A nil Payment is exactly InscribeFile.
func (in *Inscriber) InscribeFilePaying(ctx context.Context, meta protocol.MintWire, content []byte, mode protocol.PlasmaMode, to types.Address, pay *Payment) (types.Hash, error) {
	plan, err := protocol.BuildPlan(meta, content, mode)
	if err != nil {
		return types.ZeroHash, err
	}

	order := plan.SubmitOrder()
	in.log.Info("inscribing",
		"bytes", len(content), "blocks", len(order), "mode", mode, "mime", meta.Mime)

	var (
		prevHash types.Hash // hash of the part published just before this one
		tailHash types.Hash // hash of the very first part published
	)

	for i, part := range order {
		var ph, th []byte
		switch part.Role {
		case protocol.RoleHead:
			ph, th = prevHash.Bytes(), tailHash.Bytes()
		case protocol.RoleIntermediary:
			ph = prevHash.Bytes()
		}

		data, err := plan.EncodePart(part, ph, th)
		if err != nil {
			return types.ZeroHash, fmt.Errorf("encode %s: %w", part.Role, err)
		}

		// Only the head carries the payment. Intermediary and tail parts are
		// plain sends: they have no asset id of their own, so value on one
		// would be value no deriver can attribute to anything.
		partTo, amount, zts := to, big.NewInt(0), types.ZnnTokenStandard
		if pay != nil && (part.Role == protocol.RoleHead || part.Role == protocol.RoleStandalone) {
			partTo, amount, zts = pay.To, pay.Amount, pay.Token
		}

		hash, err := in.Send(ctx, partTo, amount, zts, data)
		if err != nil {
			return types.ZeroHash, fmt.Errorf("publish %s (%d/%d): %w",
				part.Role, i+1, len(order), err)
		}
		in.log.Info("published part",
			"role", part.Role, "n", fmt.Sprintf("%d/%d", i+1, len(order)),
			"bytes", len(data), "hash", hash)

		if part.Role == protocol.RoleHead || part.Role == protocol.RoleStandalone {
			return hash, nil
		}

		// The next part names this one, so this one has to be on chain first.
		if _, err := in.WaitForConfirmation(ctx, hash); err != nil {
			return types.ZeroHash, err
		}
		if i == 0 {
			tailHash = hash
		}
		prevHash = hash
	}
	return types.ZeroHash, fmt.Errorf("plan produced no head")
}

// --- simple opcode helpers ---

func (in *Inscriber) Transfer(ctx context.Context, assetID types.Hash, amount uint64, to types.Address) (types.Hash, error) {
	payload, err := protocol.Marshal(&protocol.TransferWire{Asset: assetID.Bytes(), Amount: amount})
	if err != nil {
		return types.ZeroHash, err
	}
	return in.sendZDU(ctx, protocol.OpTransfer, payload, to, big.NewInt(0), types.ZnnTokenStandard)
}

func (in *Inscriber) CreateCollection(ctx context.Context, c protocol.CollectionWire) (types.Hash, error) {
	if err := c.Validate(); err != nil {
		return types.ZeroHash, err
	}
	payload, err := protocol.Marshal(&c)
	if err != nil {
		return types.ZeroHash, err
	}
	return in.sendZDU(ctx, protocol.OpCreateCollection, payload, in.addr, big.NewInt(0), types.ZnnTokenStandard)
}

func (in *Inscriber) CreateDispenser(ctx context.Context, d protocol.DispenserWire) (types.Hash, error) {
	if err := d.Validate(); err != nil {
		return types.ZeroHash, err
	}
	payload, err := protocol.Marshal(&d)
	if err != nil {
		return types.ZeroHash, err
	}
	return in.sendZDU(ctx, protocol.OpCreateDispenser, payload, in.addr, big.NewInt(0), types.ZnnTokenStandard)
}

func (in *Inscriber) CancelDispenser(ctx context.Context, id types.Hash) (types.Hash, error) {
	payload, err := protocol.Marshal(&protocol.DispenserRefWire{Dispenser: id.Bytes()})
	if err != nil {
		return types.ZeroHash, err
	}
	return in.sendZDU(ctx, protocol.OpCancelDispenser, payload, in.addr, big.NewInt(0), types.ZnnTokenStandard)
}

// Purchase pays the seller in the carrier block itself: a dispenser purchase
// is the one opcode where the block's own value transfer is the settlement,
// so the payment must go to the dispenser's issuer.
func (in *Inscriber) Purchase(ctx context.Context, id types.Hash, seller types.Address, amount *big.Int, zts types.ZenonTokenStandard) (types.Hash, error) {
	payload, err := protocol.Marshal(&protocol.DispenserRefWire{Dispenser: id.Bytes()})
	if err != nil {
		return types.ZeroHash, err
	}
	return in.sendZDU(ctx, protocol.OpDispenserPurchase, payload, seller, amount, zts)
}

func (in *Inscriber) sendZDU(ctx context.Context, op protocol.Opcode, payload []byte, to types.Address, amount *big.Int, zts types.ZenonTokenStandard) (types.Hash, error) {
	z := &protocol.ZDU{Prefix: protocol.Prefix, Opcode: op, Version: protocol.Version, Data: payload}
	data, err := z.Encode()
	if err != nil {
		return types.ZeroHash, err
	}
	return in.Send(ctx, to, amount, zts, data)
}

// HexHash parses a hash the CLI received as a hex string.
func HexHash(s string) (types.Hash, error) {
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != protocol.HashLen {
		return types.ZeroHash, fmt.Errorf("expected 64 hex characters, got %q", s)
	}
	var h types.Hash
	copy(h[:], raw)
	return h, nil
}

// Quote is what an inscription is about to cost, before it is published.
//
// It exists because the EVM's fee has to be decided *before* signing and there
// are no refunds: quoting from stale numbers means burning ZNN for less gas
// than was asked for, with no way to top up.
type Quote struct {
	// BlockBytes is the exact size of the head block's data field — the whole
	// envelope, not the payload — because that is what plasma is charged on.
	BlockBytes int
	// BasePlasma is the nominal cost of those bytes.
	BasePlasma uint64
	// FusionPrice is the resource price the next momentum will charge, in
	// units of protocol.PriceScaleFactor.
	FusionPrice uint64
	// PaidPlasma is what the block will actually have to pay:
	// ceil(BasePlasma x FusionPrice / PriceScaleFactor).
	PaidPlasma uint64
}

// Quote sizes an inscription and reads the resource price it will meet.
//
// The size is computed by encoding the head part rather than estimating it,
// because the fee is quoted against BasePlasma and BasePlasma is a function of
// the exact byte count — an estimate that is one byte out quotes a fee for a
// congestion multiplier the deriver will not compute.
//
// Multi-part chains are out of scope and say so: the fee rides the head, whose
// size depends on hashes that do not exist until the tail is published, so a
// quote taken now would be for a block that cannot be built yet.
func (in *Inscriber) Quote(ctx context.Context, meta protocol.MintWire, content []byte, mode protocol.PlasmaMode) (*Quote, error) {
	plan, err := protocol.BuildPlan(meta, content, mode)
	if err != nil {
		return nil, err
	}
	order := plan.SubmitOrder()
	if len(order) != 1 || order[0].Role != protocol.RoleStandalone {
		return nil, fmt.Errorf("this inscription needs %d blocks; a fee can only be quoted for a single-block mint", len(order))
	}
	data, err := plan.EncodePart(order[0], nil, nil)
	if err != nil {
		return nil, err
	}

	q := &Quote{
		BlockBytes:  len(data),
		BasePlasma:  protocol.PlasmaForDataLength(len(data)),
		FusionPrice: protocol.MinResourcePrice,
	}
	// The price the *next* momentum charges is what this block will meet, and
	// the frontier is where that is published. A node that reports none is
	// pre-spork, and the floor is the right reading.
	if m, err := in.client.FrontierMomentum(ctx); err == nil && m != nil && m.NextFusionPrice > protocol.MinResourcePrice {
		q.FusionPrice = m.NextFusionPrice
	}

	// The same arithmetic payPlasma uses, deliberately shared: a quote that
	// priced the block differently from the way it is paid for would be a fee
	// computed against a congestion multiplier the deriver never sees.
	paid, err := requiredFusedPlasma(q.BasePlasma, q.FusionPrice)
	if err != nil {
		return nil, err
	}
	q.PaidPlasma = in.withHeadroom(paid)
	return q, nil
}

// withHeadroom pads a required fused-plasma figure by HeadroomPercent,
// rounding up.
func (in *Inscriber) withHeadroom(required uint64) uint64 {
	if in.HeadroomPercent == 0 {
		return required
	}
	return required + (required*in.HeadroomPercent+99)/100
}

// ErrNotFrontier is Rebid refusing a block that is not its account's
// frontier: it has descendants, or a competing bid for its height won.
var ErrNotFrontier = errors.New("block is not its account's frontier")

// Rebid re-signs a pending block at its own height and the current price, so
// the node replaces it, and returns the replacement's hash. ok is false when
// there was nothing to do: the block is already confirmed, or the price has
// not risen past what it paid.
//
// go-zenon's account pool accepts a block at an occupied height when it pays a
// higher price, and rolls back the one it displaces (chain/account_pool.go,
// higherPriority). That is the only way out for a block that missed a
// congested momentum: headroom keeps it valid for a momentum or two, but the
// content selector admits the highest price first, and blocks signed later are
// signed at the later, higher price — so under sustained overload the oldest
// block loses every time until it bids again.
//
// The replacement carries the same recipient, amount and data, with one
// exception. A burn to protocol.BurnAddress is an EVM fee, and the deriver
// prices it at paid/base plasma, so paying more plasma for the same burn
// would buy less gas. The burn is scaled by the same ratio as the plasma,
// rounding up, so the call keeps at least the gas it had.
//
// Only the frontier can be rebid. Replacing a block with descendants rolls
// the descendants back too, which for a multi-part inscription or a run of
// back-to-back sends means silently discarding blocks the caller believes
// are published.
func (in *Inscriber) Rebid(ctx context.Context, stuck types.Hash) (types.Hash, bool, error) {
	// The frontier includes unconfirmed blocks, so it is both the check that
	// nothing builds on this one and the copy of the block to re-sign.
	old, err := in.client.FrontierAccountBlock(ctx, in.addr)
	if err != nil || old == nil {
		return stuck, false, fmt.Errorf("frontier: %v", err)
	}
	if old.Hash != stuck {
		return stuck, false, ErrNotFrontier
	}
	if old.ConfirmationDetail != nil && old.ConfirmationDetail.MomentumHeight > 0 {
		return stuck, false, nil
	}
	if old.BlockType != blockTypeUserSend {
		return stuck, false, fmt.Errorf("only sends can be rebid")
	}
	momentum, err := in.client.FrontierMomentum(ctx)
	if err != nil {
		return stuck, false, fmt.Errorf("frontier momentum: %w", err)
	}

	amount := big.NewInt(0)
	if old.Amount.Int != nil {
		amount.Set(old.Amount.Int)
	}
	block := &nom.AccountBlock{
		Version:              blockVersion,
		ChainIdentifier:      in.chain,
		BlockType:            blockTypeUserSend,
		PreviousHash:         old.PreviousHash,
		Height:               old.Height,
		MomentumAcknowledged: types.HashHeight{Hash: momentum.Hash, Height: momentum.Height},
		Address:              in.addr,
		ToAddress:            old.ToAddress,
		Amount:               amount,
		TokenStandard:        old.TokenStandard,
		FromBlockHash:        types.ZeroHash,
		Data:                 old.Data,
		PublicKey:            in.key.Public,
	}
	if err := in.payPlasma(ctx, block, momentum.NextFusionPrice); err != nil {
		return stuck, false, err
	}
	oldPaid := old.FusedPlasma + protocol.DifficultyToPlasma(old.Difficulty)
	newPaid := block.FusedPlasma + protocol.DifficultyToPlasma(block.Difficulty)
	if newPaid <= oldPaid {
		return stuck, false, nil // the pool would keep the old one anyway
	}
	if old.ToAddress == protocol.BurnAddress && amount.Sign() > 0 {
		// The fee buys gas at max(paid, base)/base; never scale from below
		// the floor, where the old burn was priced at 1.0x.
		from := max(oldPaid, protocol.PlasmaForDataLength(len(old.Data)))
		scaled := new(big.Int).Mul(amount, new(big.Int).SetUint64(newPaid))
		scaled.Add(scaled, new(big.Int).SetUint64(from-1))
		block.Amount = scaled.Div(scaled, new(big.Int).SetUint64(from))
	}

	block.Hash = block.ComputeHash()
	block.Signature = in.key.Sign(block.Hash.Bytes())
	if err := in.client.PublishRawTransaction(ctx, block); err != nil {
		return stuck, false, fmt.Errorf("publish rebid: %w", err)
	}
	in.log.Info("rebid a pending block", "height", block.Height, "replaces", stuck,
		"hash", block.Hash, "paid_plasma", newPaid, "was", oldPaid, "amount", block.Amount)
	return block.Hash, true, nil
}

// ConfirmRebidding is WaitForConfirmation for a frontier block that may be
// left out of a congested momentum: every `after` without a confirmation it
// rebids (see Rebid), and it returns the hash that finally confirmed.
//
// That is not necessarily the newest bid. Replacement is a per-node pool
// decision: the node this client talks to accepts the rebid, but the pillar
// that produces the next momentum may still hold the original and commit it
// (2 of 6 rebids in the 128-wallet benchmark re-run went that way). Exactly
// one block per height ever confirms, so every bid is watched and whichever
// lands is the answer.
func (in *Inscriber) ConfirmRebidding(ctx context.Context, hash types.Hash, after time.Duration) (types.Hash, uint64, int, error) {
	deadline := time.Now().Add(in.ConfirmWait)
	last := time.Now()
	bids := []types.Hash{hash}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return hash, 0, len(bids) - 1, ctx.Err()
		case <-time.After(time.Second):
		}
		for _, h := range bids {
			if b, err := in.client.AccountBlockByHash(ctx, h); err == nil && b != nil &&
				b.ConfirmationDetail != nil && b.ConfirmationDetail.MomentumHeight > 0 {
				return h, b.ConfirmationDetail.MomentumHeight, len(bids) - 1, nil
			}
		}
		if after <= 0 || time.Since(last) < after {
			continue
		}
		last = time.Now()
		newest := bids[len(bids)-1]
		next, ok, err := in.Rebid(ctx, newest)
		switch {
		case errors.Is(err, ErrNotFrontier):
			// Another bid for this height already won, and the loop above
			// sees it confirm next round.
			in.log.Debug("rebid skipped: the height is already settled", "hash", newest)
		case err != nil:
			in.log.Warn("rebid failed; still waiting on the earlier bids", "hash", newest, "err", err)
		case ok:
			bids = append(bids, next)
		}
	}
	return hash, 0, len(bids) - 1, fmt.Errorf("block %s not confirmed within %s (%d rebids)", hash, in.ConfirmWait, len(bids)-1)
}

// ChainID is the network this inscriber publishes to. Callers need it to look
// up rules that differ per chain — the EVM's fee activation height, most of
// all, which must never be a flag.
func (in *Inscriber) ChainID() uint64 { return in.chain }

// requiredFusedPlasma is ceil(basePlasma x fusionPrice / PriceScaleFactor) —
// what dp.ValidPrice compares a fully-fused block against.
//
// big.Int because the product overflows uint64 at a high enough price, and
// rounding up because the comparison is >=: one unit short is a refused block.
func requiredFusedPlasma(basePlasma, fusionPrice uint64) (uint64, error) {
	if fusionPrice <= protocol.MinResourcePrice {
		// At or below the floor — and a pre-spork node reports zero — the
		// nominal figure is what is owed.
		return basePlasma, nil
	}
	n := new(big.Int).Mul(new(big.Int).SetUint64(basePlasma), new(big.Int).SetUint64(fusionPrice))
	n.Add(n, new(big.Int).SetUint64(protocol.PriceScaleFactor-1))
	n.Div(n, new(big.Int).SetUint64(protocol.PriceScaleFactor))
	if !n.IsUint64() {
		return 0, fmt.Errorf("resource price %d makes a %d-plasma block unaffordable", fusionPrice, basePlasma)
	}
	return n.Uint64(), nil
}
