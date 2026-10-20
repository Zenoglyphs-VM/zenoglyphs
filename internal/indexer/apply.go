package indexer

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// ztsDisplay converts the 10 raw bytes a dispenser carries on the wire into
// the bech32 form the node reports on a payment block, so the two can be
// compared as the same thing.
func ztsDisplay(hexZTS string) (string, error) {
	raw, err := hex.DecodeString(hexZTS)
	if err != nil {
		return "", err
	}
	zts, err := types.BytesToZTS(raw)
	if err != nil {
		return "", err
	}
	return zts.String(), nil
}

// Apply runs one event's opcode rules and updates the projections. It reports
// whether the event changed state.
//
// "Invalid" here never means "error". A block that breaks a protocol rule is
// simply not state-changing: it stays in the event log, because the log
// records what the chain contained, and it is skipped by the projection,
// because the rules say it does nothing. Only a genuine storage failure is
// returned as an error — anything else would let one malformed block from an
// anonymous sender halt every indexer on the network.
//
// The returned bool is what the state root commits to. Recording it is what
// makes the commitment able to see a rules disagreement rather than only an
// encoding one.
func (ix *Indexer) Apply(e store.Event) (bool, error) {
	var err error
	switch protocol.Opcode(e.Opcode) {
	case protocol.OpMint:
		err = ix.applyMint(e)
	case protocol.OpTransfer:
		err = ix.applyTransfer(e)
	case protocol.OpMulti:
		err = ix.applyMulti(e)
	case protocol.OpCreateCollection:
		err = ix.applyCollection(e)
	case protocol.OpCreateDispenser:
		err = ix.applyCreateDispenser(e)
	case protocol.OpCancelDispenser:
		err = ix.applyCancelDispenser(e)
	case protocol.OpDispenserPurchase:
		err = ix.applyPurchase(e)
	default:
		return false, nil
	}
	if err != nil {
		if errors.Is(err, errInvalid) {
			ix.log.Debug("event rejected by protocol rules",
				"op", protocol.Opcode(e.Opcode), "hash", e.BlockHash, "reason", err)
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// errInvalid marks a protocol-rule rejection, as opposed to a storage fault.
var errInvalid = errors.New("invalid")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInvalid, fmt.Sprintf(format, args...))
}

// zdu rebuilds the envelope view an opcode handler needs from a stored event.
func zduOf(e store.Event) *protocol.ZDU {
	ph, _ := hex.DecodeString(e.PHash)
	th, _ := hex.DecodeString(e.THash)
	return &protocol.ZDU{
		Prefix: protocol.Prefix, Opcode: protocol.Opcode(e.Opcode),
		Version: e.Version, Data: e.Payload, PHash: ph, THash: th,
		Ext: e.Ext,
	}
}

// --- op 0: mint ---

func (ix *Indexer) applyMint(e store.Event) error {
	z := zduOf(e)
	m, err := z.DecodeMint()
	if err != nil {
		return invalid("mint payload: %v", err)
	}

	multipart := len(z.PHash) > 0 || len(z.THash) > 0
	content := m.Data
	parts := 1

	if multipart {
		// A head must carry both hashes. One without the other describes a
		// chain with no defined end, which no two indexers would walk alike.
		if len(z.PHash) == 0 || len(z.THash) == 0 {
			return invalid("multi-part head must carry both pHash and tHash")
		}
		assembled, n, err := ix.assemble(e, m.Data)
		if err != nil {
			return err
		}
		content, parts = assembled, n
	} else if len(m.Data) == 0 {
		return invalid("standalone mint carries no content")
	}

	// A collection reference binds only if the same account issued both.
	// Without this, anyone can inscribe themselves into any collection. The
	// asset stays valid — only the claimed membership is dropped.
	collectionID := ""
	if len(m.Collection) > 0 {
		cid := hex.EncodeToString(m.Collection)
		col, err := ix.st.Collection(cid)
		if err != nil {
			return err
		}
		if col != nil && col.Issuer == e.Sender {
			collectionID = cid
		} else {
			ix.log.Debug("dropping unowned collection reference",
				"asset", e.BlockHash, "collection", cid)
		}
	}

	number, err := ix.st.NextAssetNumber()
	if err != nil {
		return err
	}

	asset := store.Asset{
		ID: e.BlockHash, Number: number,
		Issuer: e.Sender,
		// The recipient of the carrier block owns the supply. A mint to
		// yourself is the ordinary case; a mint to someone else issues
		// directly to them.
		Owner:        e.Recipient,
		CollectionID: collectionID,
		Name:         m.Name, Description: m.Description,
		Supply: m.Supply, Decimals: m.Decimals, Mime: m.Mime,
		Attributes: m.Attributes, Ext: m.Ext, Content: content, PartCount: parts,
		Height: e.Height, MomentumIndex: e.MomentumIndex, Timestamp: e.Timestamp,
	}
	if err := ix.st.PutAsset(asset); err != nil {
		return err
	}
	// Composite imports are recorded as edges and never resolved: the indexer
	// does not fetch a target, does not require one to exist, and no state
	// depends on any of it. Requiring the target to be present would make
	// validity depend on how far an indexer had synced (spec R-4.7.2).
	if len(m.Refs) > 0 {
		refs := make([]string, len(m.Refs))
		for i, r := range m.Refs {
			refs[i] = hex.EncodeToString(r)
		}
		if err := ix.st.PutRefs(asset.ID, e.Height, refs); err != nil {
			return err
		}
	}
	return ix.st.SetBalance(asset.ID, asset.Owner, asset.Supply)
}

// assemble walks a multi-part chain from the head and returns the content.
//
// The walk is head-first along pHash, terminating when it reaches tHash. Four
// rules bound it, and all four exist because the walk follows attacker-chosen
// pointers:
//
//   - Same issuer throughout. Without it, anyone can point a head into
//     someone else's chunks and mint a duplicate for the cost of one block.
//     This is the most important of the four.
//   - Every part strictly earlier in canonical order than the head, so a
//     chain can never depend on a block that has not happened yet.
//   - Depth capped at MaxAssetParts.
//   - Cycle detection, because a pHash loop would otherwise walk forever.
func (ix *Indexer) assemble(head store.Event, first []byte) ([]byte, int, error) {
	content := append([]byte(nil), first...)
	seen := map[string]bool{head.BlockHash: true}
	cur := head.PHash
	count := 1

	for {
		if count >= protocol.MaxAssetParts {
			return nil, 0, invalid("chain exceeds %d parts", protocol.MaxAssetParts)
		}
		if seen[cur] {
			return nil, 0, invalid("cycle in pHash chain at %s", cur)
		}
		seen[cur] = true

		p, err := ix.st.Part(cur)
		if err != nil {
			return nil, 0, err
		}
		if p == nil {
			return nil, 0, invalid("missing part %s", cur)
		}
		if p.Issuer != head.Sender {
			return nil, 0, invalid("part %s issued by %s, head by %s", cur, p.Issuer, head.Sender)
		}
		if !earlier(p.Height, p.MomentumIndex, head.Height, head.MomentumIndex) {
			return nil, 0, invalid("part %s does not precede the head in canonical order", cur)
		}

		content = append(content, p.Chunk...)
		count++

		if cur == head.THash {
			break
		}
		if p.PHash == "" {
			return nil, 0, invalid("chain ended at %s before reaching tHash", cur)
		}
		cur = p.PHash
	}

	// The first head in canonical order consumes the tail; every later head
	// naming the same tail is invalid. Replay is what makes "first"
	// well-defined here — the claim table is rebuilt in the same order.
	ok, err := ix.st.ClaimTail(head.THash, head.BlockHash, head.Height)
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		return nil, 0, invalid("tail %s already consumed by another head", head.THash)
	}
	return content, count, nil
}

func earlier(h1 uint64, i1 int, h2 uint64, i2 int) bool {
	if h1 != h2 {
		return h1 < h2
	}
	return i1 < i2
}

// --- op 2: multi ---

// applyMulti records a chunk. It never creates state on its own: a chunk with
// no head is inert, and stays inert forever if no head ever names it.
func (ix *Indexer) applyMulti(e store.Event) error {
	z := zduOf(e)
	chunk, err := z.DecodeChunk()
	if err != nil {
		return invalid("chunk payload: %v", err)
	}
	if len(z.THash) > 0 {
		return invalid("a chunk may not carry tHash")
	}
	return ix.st.PutPart(store.Part{
		BlockHash: e.BlockHash, Issuer: e.Sender,
		Height: e.Height, MomentumIndex: e.MomentumIndex,
		PHash: e.PHash, Chunk: chunk,
	})
}

// --- op 1: transfer ---

func (ix *Indexer) applyTransfer(e store.Event) error {
	t, err := zduOf(e).DecodeTransfer()
	if err != nil {
		return invalid("transfer payload: %v", err)
	}
	assetID := hex.EncodeToString(t.Asset)

	asset, err := ix.st.Asset(assetID)
	if err != nil {
		return err
	}
	if asset == nil {
		return invalid("transfer of unknown asset %s", assetID)
	}

	// A send is unilateral on NoM, so the transfer settles when the send
	// block confirms. The recipient never has to act, and a transfer to an
	// address that never receives anything is still final.
	burn := e.Recipient == protocol.BurnAddress.String()
	if burn {
		if err := ix.st.Burn(assetID, e.Sender, t.Amount); err != nil {
			if errors.Is(err, store.ErrInsufficient) {
				return invalid("burn exceeds balance")
			}
			return err
		}
	} else {
		// A self-send is a valid no-op: Move debits and credits the same
		// address, which nets to zero but still requires a sufficient
		// balance, exactly as a transfer to anyone else would.
		if err := ix.st.Move(assetID, e.Sender, e.Recipient, t.Amount); err != nil {
			if errors.Is(err, store.ErrInsufficient) {
				return invalid("transfer exceeds balance")
			}
			return err
		}
	}

	return ix.st.PutTransfer(store.Transfer{
		Height: e.Height, MomentumIndex: e.MomentumIndex, BlockHash: e.BlockHash,
		AssetID: assetID, Sender: e.Sender, Recipient: e.Recipient,
		Amount: t.Amount, Burned: burn, Timestamp: e.Timestamp,
	})
}

// --- op 3: createCollection ---

func (ix *Indexer) applyCollection(e store.Event) error {
	c, err := zduOf(e).DecodeCollection()
	if err != nil {
		return invalid("collection payload: %v", err)
	}
	return ix.st.PutCollection(store.Collection{
		ID: e.BlockHash, Issuer: e.Sender,
		Name: c.Name, Description: c.Description, URL: c.URL,
		Height: e.Height, MomentumIndex: e.MomentumIndex, Timestamp: e.Timestamp,
	})
}

// --- op 4/5/6: dispensers ---

// escrowHolder is the pseudo-address that holds a dispenser's stock while it
// is for sale. Using the dispenser id keeps escrow in the same balance table
// as everything else, so "supply is conserved" stays a single invariant
// instead of two accounting systems that have to agree.
func escrowHolder(dispenserID string) string { return dispenserID }

func (ix *Indexer) applyCreateDispenser(e store.Event) error {
	d, err := zduOf(e).DecodeDispenser()
	if err != nil {
		return invalid("dispenser payload: %v", err)
	}
	assetID := hex.EncodeToString(d.Asset)
	asset, err := ix.st.Asset(assetID)
	if err != nil {
		return err
	}
	if asset == nil {
		return invalid("dispenser for unknown asset %s", assetID)
	}

	// Escrow: the stock leaves the seller's spendable balance now, so it
	// cannot be sold twice or transferred out from under a buyer.
	if err := ix.st.Move(assetID, e.Sender, escrowHolder(e.BlockHash), d.Amount); err != nil {
		if errors.Is(err, store.ErrInsufficient) {
			return invalid("dispenser exceeds seller balance")
		}
		return err
	}

	return ix.st.PutDispenser(store.Dispenser{
		ID: e.BlockHash, Issuer: e.Sender, AssetID: assetID,
		Remaining: d.Amount, Price: d.Price,
		ZTS:    hex.EncodeToString(d.ZTS),
		Height: e.Height, MomentumIndex: e.MomentumIndex, Timestamp: e.Timestamp,
	})
}

func (ix *Indexer) applyCancelDispenser(e store.Event) error {
	ref, err := zduOf(e).DecodeDispenserRef()
	if err != nil {
		return invalid("cancel payload: %v", err)
	}
	id := hex.EncodeToString(ref.Dispenser)
	d, err := ix.st.Dispenser(id)
	if err != nil {
		return err
	}
	if d == nil {
		return invalid("cancel of unknown dispenser %s", id)
	}
	if d.Issuer != e.Sender {
		return invalid("only the issuer may cancel a dispenser")
	}
	if d.Cancelled {
		return invalid("dispenser already cancelled")
	}
	if d.Remaining > 0 {
		if err := ix.st.Move(d.AssetID, escrowHolder(id), d.Issuer, d.Remaining); err != nil {
			return err
		}
	}
	return ix.st.UpdateDispenser(id, 0, true)
}

func (ix *Indexer) applyPurchase(e store.Event) error {
	ref, err := zduOf(e).DecodeDispenserRef()
	if err != nil {
		return invalid("purchase payload: %v", err)
	}
	id := hex.EncodeToString(ref.Dispenser)
	d, err := ix.st.Dispenser(id)
	if err != nil {
		return err
	}
	if d == nil {
		return invalid("purchase from unknown dispenser %s", id)
	}
	if d.Cancelled || d.Remaining == 0 {
		return invalid("dispenser is closed")
	}

	// The payment must actually reach the seller. Checking only that a
	// payment exists lets a buyer pay themselves and claim the asset — the
	// recipient check is the entire settlement.
	if e.Recipient != d.Issuer {
		return invalid("payment went to %s, not the dispenser issuer %s", e.Recipient, d.Issuer)
	}
	want, err := ztsDisplay(d.ZTS)
	if err != nil {
		return invalid("dispenser records an unparseable token standard")
	}
	if e.TokenStandard != want {
		return invalid("payment token %s does not match dispenser token %s",
			e.TokenStandard, want)
	}

	paid, ok := new(big.Int).SetString(e.Amount, 10)
	if !ok {
		return invalid("unparseable payment amount %q", e.Amount)
	}
	price := new(big.Int).SetUint64(d.Price)
	if price.Sign() == 0 || paid.Cmp(price) < 0 {
		return invalid("payment %s below price %s", paid, price)
	}

	qty := new(big.Int).Div(paid, price)
	if !qty.IsUint64() || qty.Uint64() > d.Remaining {
		// No partial fills. There is no refund path on NoM, so filling part
		// of an order would silently overcharge the buyer for units that do
		// not exist.
		return invalid("purchase of %s exceeds remaining %d", qty, d.Remaining)
	}
	n := qty.Uint64()

	if err := ix.st.Move(d.AssetID, escrowHolder(id), e.Sender, n); err != nil {
		if errors.Is(err, store.ErrInsufficient) {
			return invalid("dispenser escrow is short")
		}
		return err
	}
	if err := ix.st.UpdateDispenser(id, d.Remaining-n, false); err != nil {
		return err
	}

	// The remainder below one unit is a tip to the seller, not change.
	return ix.st.PutTransfer(store.Transfer{
		Height: e.Height, MomentumIndex: e.MomentumIndex, BlockHash: e.BlockHash,
		AssetID: d.AssetID, Sender: escrowHolder(id), Recipient: e.Sender,
		Amount: n, Timestamp: e.Timestamp,
	})
}
