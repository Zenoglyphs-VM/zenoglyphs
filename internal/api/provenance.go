package api

import (
	"encoding/hex"
	"sort"
	"strconv"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/store"
)

// The activity feed, and the two things the transfers projection does not
// contain.
//
// A transfer row is written when applyTransfer or applyPurchase runs. Three
// balance movements are not either of those, so nothing records them:
//
//	the mint            supply appears at the owner. SetBalance, no row.
//	escrow in           createDispenser moves stock out of the seller. Move,
//	                    no row — so a purchase looks like it arrived from an
//	                    address that was never funded.
//	escrow out          cancelDispenser moves it back. Move, no row.
//
// All three are reconstructed here, at serve time, from the event log and the
// dispensers projection. Writing them in the indexer instead would put them
// in the transfers table, which Store.ProjectionDigest hashes over — so an
// indexer that had upgraded and one that had not would report a divergence
// that is not one, and `zcribe compare` would be crying wolf until the last
// operator upgraded. Moderation is excluded from the state root for the same
// reason; this is that precedent applied to provenance.
//
// A reconstructed row is flagged `synthetic` and carries the hash of the real
// block that caused the movement, so nothing here has to be taken on trust:
// every row still points at something on the chain.

// Row kinds. These name what happened, which "from" and "to" alone cannot say
// — a purchase and a plain transfer differ only in who the sender is, and the
// sender of a purchase is a pseudo-address.
const (
	kindMint     = "mint"     // issuance: the supply's first appearance
	kindTransfer = "transfer" // an ordinary op=1 send
	kindBurn     = "burn"     // op=1 to the burn sink
	kindEscrow   = "escrow"   // stock into a dispenser, on create
	kindRelease  = "release"  // stock back to the issuer, on cancel
	kindSale     = "sale"     // a dispenser fill: escrow to the buyer
)

type transferJSON struct {
	Kind          string `json:"kind"`
	Height        uint64 `json:"momentum"`
	MomentumIndex int    `json:"momentumIndex"`
	Block         string `json:"block"`
	From          string `json:"from"`
	To            string `json:"to"`
	Amount        string `json:"amount"`
	Burned        bool   `json:"burned"`
	Timestamp     int64  `json:"timestamp"`

	// FromDispenser and ToDispenser name the dispenser behind an escrow
	// party. The address in From/To is a display form and cannot be resolved
	// back to an id (see protocol.EscrowAddress), so the id travels beside
	// it: that is what a client needs to link the row to the listing.
	FromDispenser string `json:"fromDispenser,omitempty"`
	ToDispenser   string `json:"toDispenser,omitempty"`

	// Synthetic marks a row reconstructed here rather than read from the
	// transfers projection. Two indexers must agree on every row without
	// it; a row with it is this indexer's reading of the log, and saying so
	// is what keeps the feed honest about which is which.
	Synthetic bool `json:"synthetic,omitempty"`
}

// escrowParties maps the pseudo-addresses an asset's balances can be held
// under to the dispensers they belong to.
//
// The indexer keys escrow by the dispenser's own id, which is a 64-hex block
// hash sitting in a column of z1 addresses. Rendering it raw is what made a
// purchase look like it came from an account nobody could find. The map is
// built from the dispensers table rather than by pattern-matching the string,
// so an address is called escrow because a dispenser with that id exists, not
// because it looked like a hash.
func (s *Server) escrowParties(assetID string) (map[string]string, error) {
	ds, err := s.st.DispensersOfAsset(assetID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ds))
	for _, d := range ds {
		out[d.ID] = protocol.EscrowAddress(d.ID)
	}
	return out, nil
}

// party renders one side of a balance movement: an escrow key becomes its
// z1zen0glyphx address, and anything else is already an address.
func party(escrow map[string]string, key string) (display, dispenser string) {
	if addr, ok := escrow[key]; ok {
		return addr, key
	}
	return key, ""
}

// transferFeed is the asset's activity, newest first: every recorded transfer
// plus the reconstructed rows, merged into one canonical order.
func (s *Server) transferFeed(a *store.Asset, limit int) ([]transferJSON, error) {
	escrow, err := s.escrowParties(a.ID)
	if err != nil {
		return nil, err
	}

	recorded, err := s.st.TransfersOf(a.ID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]transferJSON, 0, len(recorded)+2*len(escrow)+1)
	for _, t := range recorded {
		from, fromDisp := party(escrow, t.Sender)
		to, toDisp := party(escrow, t.Recipient)
		kind := kindTransfer
		switch {
		case t.Burned:
			kind = kindBurn
		case fromDisp != "":
			// Units leaving escrow to a buyer. The only other way out of
			// escrow is a cancel, and that has no transfer row at all.
			kind = kindSale
		}
		out = append(out, transferJSON{
			Kind: kind, Height: t.Height, MomentumIndex: t.MomentumIndex,
			Block: t.BlockHash, From: from, To: to,
			Amount: strconv.FormatUint(t.Amount, 10), Burned: t.Burned,
			Timestamp: t.Timestamp, FromDispenser: fromDisp, ToDispenser: toDisp,
		})
	}

	legs, err := s.escrowLegs(a)
	if err != nil {
		return nil, err
	}
	out = append(out, legs...)

	// Issuance. The mint block is a real send — its hash is the asset id, its
	// recipient owns the supply — so this row says exactly what the block
	// did. A self-mint shows the issuer on both sides, which is what
	// happened.
	out = append(out, transferJSON{
		Kind: kindMint, Height: a.Height, MomentumIndex: a.MomentumIndex,
		Block: a.ID, From: a.Issuer, To: a.Owner,
		Amount: strconv.FormatUint(a.Supply, 10), Timestamp: a.Timestamp,
		Synthetic: true,
	})

	// Canonical order, newest first, the same order the projection uses. The
	// limit is applied after the merge: the reconstructed rows are part of
	// the feed, not an appendix to it, and the mint is the oldest row there
	// is — so on a long feed it falls off the end like any other old event.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Height != out[j].Height {
			return out[i].Height > out[j].Height
		}
		return out[i].MomentumIndex > out[j].MomentumIndex
	})
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// escrowLegs reconstructs the two movements a dispenser makes outside the
// transfers table: stock in when it opens, stock back out if it is cancelled.
func (s *Server) escrowLegs(a *store.Asset) ([]transferJSON, error) {
	ds, err := s.st.DispensersOfAsset(a.ID)
	if err != nil || len(ds) == 0 {
		return nil, err
	}

	cancels, err := s.cancelEvents(ds)
	if err != nil {
		return nil, err
	}

	out := make([]transferJSON, 0, 2*len(ds))
	for _, d := range ds {
		addr := protocol.EscrowAddress(d.ID)

		// How much was escrowed is in the create payload and nowhere else:
		// the projection's `remaining` counts down as units sell and is set
		// to zero by a cancel.
		escrowed, err := s.escrowedAmount(d)
		if err != nil {
			return nil, err
		}
		if escrowed > 0 {
			out = append(out, transferJSON{
				Kind: kindEscrow, Height: d.Height, MomentumIndex: d.MomentumIndex,
				Block: d.ID, From: d.Issuer, To: addr,
				Amount: strconv.FormatUint(escrowed, 10), Timestamp: d.Timestamp,
				ToDispenser: d.ID, Synthetic: true,
			})
		}

		ev, cancelled := cancels[d.ID]
		if !cancelled {
			continue
		}
		// What a cancel released is what was left: everything escrowed that
		// had not already been sold. Sales do leave transfer rows, so the
		// subtraction is over recorded history rather than a guess — and a
		// cancel is terminal, so there is nothing after it to account for.
		sold, err := s.st.TransferredFrom(a.ID, d.ID)
		if err != nil {
			return nil, err
		}
		if sold > escrowed {
			// Only reachable if the projection and the log disagree, which
			// is a bug worth not compounding with a negative amount.
			s.log.Warn("dispenser sold more than it escrowed; skipping the release row",
				"dispenser", d.ID, "escrowed", escrowed, "sold", sold)
			continue
		}
		if released := escrowed - sold; released > 0 {
			out = append(out, transferJSON{
				Kind: kindRelease, Height: ev.Height, MomentumIndex: ev.MomentumIndex,
				Block: ev.BlockHash, From: addr, To: d.Issuer,
				Amount: strconv.FormatUint(released, 10), Timestamp: ev.Timestamp,
				FromDispenser: d.ID, Synthetic: true,
			})
		}
	}
	return out, nil
}

// escrowedAmount reads the amount a dispenser locked up, from the
// createDispenser block itself.
func (s *Server) escrowedAmount(d store.Dispenser) (uint64, error) {
	ev, err := s.st.EventByBlockHash(d.ID)
	if err != nil || ev == nil {
		return 0, err
	}
	z := &protocol.ZDU{
		Prefix: protocol.Prefix, Opcode: protocol.Opcode(ev.Opcode),
		Version: ev.Version, Data: ev.Payload, Ext: ev.Ext,
	}
	w, err := z.DecodeDispenser()
	if err != nil {
		// The event applied, so it decoded once already. If it does not now,
		// the log is damaged — report nothing rather than a wrong number.
		s.log.Warn("createDispenser payload no longer decodes", "dispenser", d.ID, "err", err)
		return 0, nil
	}
	return w.Amount, nil
}

// cancelEvents finds the block that cancelled each of these dispensers.
//
// A cancel names its dispenser inside the payload, so there is no column to
// index on and the log has to be read. The cost is bounded twice over: only
// dispensers actually marked cancelled are looked for, and the scan starts
// above the oldest of them, because a dispenser cannot be cancelled before it
// is created.
func (s *Server) cancelEvents(ds []store.Dispenser) (map[string]store.Event, error) {
	want := map[string]bool{}
	floor := ^uint64(0)
	for _, d := range ds {
		if !d.Cancelled {
			continue
		}
		want[d.ID] = true
		if d.Height < floor {
			floor = d.Height
		}
	}
	if len(want) == 0 {
		return nil, nil
	}

	events, err := s.st.AppliedEventsWithOpcode(uint64(protocol.OpCancelDispenser), floor)
	if err != nil {
		return nil, err
	}
	out := make(map[string]store.Event, len(want))
	for _, e := range events {
		z := &protocol.ZDU{
			Prefix: protocol.Prefix, Opcode: protocol.Opcode(e.Opcode),
			Version: e.Version, Data: e.Payload, Ext: e.Ext,
		}
		ref, err := z.DecodeDispenserRef()
		if err != nil {
			continue
		}
		id := hex.EncodeToString(ref.Dispenser)
		if want[id] {
			// First cancel wins; a second one is invalid and never applied.
			if _, seen := out[id]; !seen {
				out[id] = e
			}
		}
	}
	return out, nil
}
