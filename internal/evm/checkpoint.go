package evm

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/fxamacker/cbor/v2"
)

// DerivationVersion names the rules a persisted checkpoint or receipt was
// derived under.
//
// Derived state is only ever a function of (inscriptions, rules). A
// checkpoint freezes the output of one version of the rules, so a build whose
// rules differ — a new opcode, a repriced one, a fee change, anything that
// could move a root or a receipt — must not resume from it. Bump this with
// any such change. A reader that finds another version throws the
// checkpoints away and replays from genesis, which is slow once and never
// wrong. (Rules that switch on at a height, like FeeFromHeight, are part of
// the version too: moving the height changes the output of the past.)
const DerivationVersion = 1

// checkpointWire is a chain without its receipts: the receipts are persisted
// one row each as they are produced (see Store.AppendEVMReceipts), because
// re-serialising the whole history into every checkpoint would make each one
// O(history) to write.
type checkpointWire struct {
	Version  int              `cbor:"1,keyasint"`
	ChainID  uint64           `cbor:"2,keyasint"`
	Applied  int              `cbor:"3,keyasint"`
	Skipped  int              `cbor:"4,keyasint"`
	Failed   int              `cbor:"5,keyasint"`
	Height   uint64           `cbor:"6,keyasint"`
	Time     uint64           `cbor:"7,keyasint"`
	Receipts int              `cbor:"8,keyasint"`
	Accounts []checkpointAcct `cbor:"9,keyasint"`
}

type checkpointAcct struct {
	Address Address          `cbor:"1,keyasint"`
	Nonce   uint64           `cbor:"2,keyasint"`
	Balance []byte           `cbor:"3,keyasint"`
	Code    []byte           `cbor:"4,keyasint"`
	Storage []checkpointSlot `cbor:"5,keyasint"`
}

type checkpointSlot struct {
	Key   string `cbor:"1,keyasint"`
	Value []byte `cbor:"2,keyasint"`
}

// deterministic, so the same state always checkpoints to the same bytes.
var checkpointEnc, _ = cbor.CoreDetEncOptions().EncMode()

// MarshalCheckpoint serialises everything about the chain except its
// receipts, whose count it records so a restore can check it was handed all
// of them. Call it only while nothing is applying to the chain.
func (c *Chain) MarshalCheckpoint() ([]byte, error) {
	w := checkpointWire{
		Version: DerivationVersion, ChainID: c.ChainID,
		Applied: c.Applied, Skipped: c.Skipped, Failed: c.Failed,
		Height: c.Height, Time: c.Time, Receipts: len(c.Receipts),
	}
	for _, a := range c.State.Accounts() {
		acc := c.State.accounts[a]
		ca := checkpointAcct{Address: a, Nonce: acc.Nonce, Balance: acc.Balance.Bytes(), Code: acc.Code}
		keys := make([]string, 0, len(acc.Storage))
		for k := range acc.Storage {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ca.Storage = append(ca.Storage, checkpointSlot{Key: k, Value: acc.Storage[k].Bytes()})
		}
		w.Accounts = append(w.Accounts, ca)
	}
	return checkpointEnc.Marshal(w)
}

// RestoreChain rebuilds a chain from a checkpoint and the receipts produced
// up to it, in order. It refuses a checkpoint from other rules or another
// network, and one whose receipt count does not match — a checkpoint that is
// not exactly what was written is replaced by a replay, never trusted.
//
// Configuration (GasCap, the supply curve, the fee height) is not in the
// checkpoint: it comes from NewChain, as it would for a replay, and
// DerivationVersion is what guarantees it is the configuration the
// checkpoint was built under.
func RestoreChain(chainID uint64, blob []byte, receipts []Receipt) (*Chain, error) {
	var w checkpointWire
	if err := cbor.Unmarshal(blob, &w); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}
	if w.Version != DerivationVersion {
		return nil, fmt.Errorf("checkpoint is derivation version %d, this build is %d", w.Version, DerivationVersion)
	}
	if w.ChainID != chainID {
		return nil, fmt.Errorf("checkpoint is for chain %d, not %d", w.ChainID, chainID)
	}
	if len(receipts) != w.Receipts {
		return nil, fmt.Errorf("checkpoint covers %d receipts, %d supplied", w.Receipts, len(receipts))
	}
	c := NewChain(chainID)
	for _, ca := range w.Accounts {
		acc := &Account{Nonce: ca.Nonce, Balance: new(big.Int).SetBytes(ca.Balance), Code: ca.Code,
			Storage: make(map[string]*big.Int, len(ca.Storage))}
		for _, s := range ca.Storage {
			acc.Storage[s.Key] = new(big.Int).SetBytes(s.Value)
		}
		c.State.accounts[ca.Address] = acc
	}
	c.Applied, c.Skipped, c.Failed = w.Applied, w.Skipped, w.Failed
	c.Height, c.Time = w.Height, w.Time
	c.Receipts = receipts
	c.byID = make(map[string]int, len(receipts))
	for i, r := range receipts {
		c.byID[r.Inscription] = i
	}
	return c, nil
}

// MarshalReceipt and UnmarshalReceipt are the persisted form of one receipt.
func MarshalReceipt(r Receipt) ([]byte, error) { return checkpointEnc.Marshal(r) }

func UnmarshalReceipt(b []byte) (Receipt, error) {
	var r Receipt
	err := cbor.Unmarshal(b, &r)
	return r, err
}
