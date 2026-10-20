package protocol

import (
	"bytes"
	"errors"
	"fmt"
)

// Chunking is a client-side packing concern, never a protocol rule: an
// indexer accepts any chain of blocks the network accepted, however it was
// split. What the protocol does constrain is the shape of the chain — which
// block carries which hash, and in what order they may appear.
//
// Every budget here is measured by encoding a probe through the real encoder.
// Estimating the overhead instead (from JSON length, or by assuming a fixed
// header) is how a head carrying two hashes silently overflows the block
// limit while the arithmetic says it fits.

var (
	ErrTooLarge  = errors.New("content does not fit within MaxAssetParts blocks")
	ErrNoRoom    = errors.New("metadata alone exceeds the block limit")
	ErrEmptyData = errors.New("no content to inscribe")
)

// Role is a part's position in a multi-part chain.
type Role int

const (
	// RoleStandalone is a complete mint in one block: op=0, no hashes.
	RoleStandalone Role = iota
	// RoleHead is op=0 carrying metadata, the first chunk, pHash and tHash.
	RoleHead
	// RoleIntermediary is op=2 carrying a chunk and pHash.
	RoleIntermediary
	// RoleTail is op=2 carrying the final chunk and no hashes. It is
	// submitted first, because every other part references a hash that must
	// already exist on chain.
	RoleTail
)

func (r Role) String() string {
	switch r {
	case RoleStandalone:
		return "standalone"
	case RoleHead:
		return "head"
	case RoleIntermediary:
		return "intermediary"
	default:
		return "tail"
	}
}

// Part is one block to submit.
type Part struct {
	Role  Role
	Chunk []byte // this part's slice of the content
	Index int    // position in content order: 0 is the head's chunk
}

// Plan is the full set of blocks for one inscription, in content order
// (head first). Submission runs in reverse — see SubmitOrder.
type Plan struct {
	Meta  MintWire // Data is empty here; each part carries its own chunk
	Parts []Part
	Mode  PlasmaMode
}

// SubmitOrder returns parts in the order they must be published: tail first,
// head last. Each part references the hash of a block that already exists, so
// the chain can only be built backwards.
//
// This also satisfies the "every part strictly precedes the head in canonical
// order" rule for free — the head is published last, so it is last in
// canonical order too.
func (p *Plan) SubmitOrder() []Part {
	out := make([]Part, len(p.Parts))
	for i, part := range p.Parts {
		out[len(p.Parts)-1-i] = part
	}
	return out
}

// BuildPlan splits content into blocks that each fit the plasma mode's
// ceiling.
func BuildPlan(meta MintWire, content []byte, mode PlasmaMode) (*Plan, error) {
	if len(content) == 0 {
		return nil, ErrEmptyData
	}
	max := mode.MaxBlockData()

	// Fast path: does the whole thing fit in one standalone mint?
	if cap0, err := mintCapacity(meta, max, nil, nil); err == nil && len(content) <= cap0 {
		return &Plan{
			Meta:  meta,
			Mode:  mode,
			Parts: []Part{{Role: RoleStandalone, Chunk: content, Index: 0}},
		}, nil
	}

	probe := bytes.Repeat([]byte{0xAA}, HashLen)
	capHead, err := mintCapacity(meta, max, probe, probe)
	if err != nil {
		return nil, err
	}
	capMid, err := chunkCapacity(max, probe)
	if err != nil {
		return nil, err
	}
	capTail, err := chunkCapacity(max, nil)
	if err != nil {
		return nil, err
	}

	parts := []Part{{Role: RoleHead, Chunk: content[:capHead], Index: 0}}
	rest := content[capHead:]

	// Intermediaries are sized to capMid; the final part becomes the tail.
	// A tail has no pHash so it could hold slightly more, and the one place
	// that is worth exploiting is the common two-block case — beyond that,
	// packing the last block tighter would only shift bytes between the last
	// two blocks without changing the block count.
	for len(rest) > 0 {
		if len(parts) >= MaxAssetParts {
			return nil, fmt.Errorf("%w: %d bytes needs more than %d blocks",
				ErrTooLarge, len(content), MaxAssetParts)
		}
		if len(rest) <= capTail {
			parts = append(parts, Part{Role: RoleTail, Chunk: rest, Index: len(parts)})
			break
		}
		n := capMid
		parts = append(parts, Part{Role: RoleIntermediary, Chunk: rest[:n], Index: len(parts)})
		rest = rest[n:]
	}
	if len(parts) > MaxAssetParts {
		return nil, fmt.Errorf("%w: %d parts", ErrTooLarge, len(parts))
	}
	return &Plan{Meta: meta, Mode: mode, Parts: parts}, nil
}

// EncodePart produces the block data for one part of a plan.
func (p *Plan) EncodePart(part Part, pHash, tHash []byte) ([]byte, error) {
	switch part.Role {
	case RoleStandalone, RoleHead:
		m := p.Meta
		m.Data = part.Chunk
		payload, err := encMode.Marshal(&m)
		if err != nil {
			return nil, err
		}
		z := &ZDU{Prefix: Prefix, Opcode: OpMint, Version: Version, Data: payload}
		if part.Role == RoleHead {
			z.PHash, z.THash = pHash, tHash
		}
		return z.Encode()
	default:
		payload, err := encMode.Marshal(part.Chunk)
		if err != nil {
			return nil, err
		}
		z := &ZDU{Prefix: Prefix, Opcode: OpMulti, Version: Version, Data: payload}
		z.PHash = pHash // nil for the tail
		return z.Encode()
	}
}

// mintCapacity is the largest chunk a mint block can carry alongside meta.
//
// It measures rather than estimates: encode a probe, take the difference as
// overhead, then re-measure because shrinking the chunk can shorten its own
// CBOR length header and give a byte or two back. The loop converges in at
// most a couple of passes and is bounded either way.
func mintCapacity(meta MintWire, max int, pHash, tHash []byte) (int, error) {
	measure := func(n int) (int, error) {
		m := meta
		m.Data = make([]byte, n)
		payload, err := encMode.Marshal(&m)
		if err != nil {
			return 0, err
		}
		z := &ZDU{Prefix: Prefix, Opcode: OpMint, Version: Version,
			Data: payload, PHash: pHash, THash: tHash}
		b, err := z.Encode()
		if err != nil {
			return 0, err
		}
		return len(b), nil
	}
	return converge(measure, max, ErrNoRoom)
}

// chunkCapacity is the largest chunk an op=2 block can carry.
func chunkCapacity(max int, pHash []byte) (int, error) {
	measure := func(n int) (int, error) {
		payload, err := encMode.Marshal(make([]byte, n))
		if err != nil {
			return 0, err
		}
		z := &ZDU{Prefix: Prefix, Opcode: OpMulti, Version: Version,
			Data: payload, PHash: pHash}
		b, err := z.Encode()
		if err != nil {
			return 0, err
		}
		return len(b), nil
	}
	return converge(measure, max, ErrNoRoom)
}

// converge finds the largest n where measure(n) <= max.
func converge(measure func(int) (int, error), max int, oob error) (int, error) {
	n := max
	for range 8 {
		got, err := measure(n)
		if err != nil {
			return 0, err
		}
		if got <= max {
			// Confirm n+1 would overflow; if not, we under-shot.
			if n >= max {
				return n, nil
			}
			next, err := measure(n + 1)
			if err != nil {
				return 0, err
			}
			if next > max {
				return n, nil
			}
			n++
			continue
		}
		n -= got - max
		if n <= 0 {
			return 0, oob
		}
	}
	if n <= 0 {
		return 0, oob
	}
	return n, nil
}
