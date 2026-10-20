package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zenon-network/go-zenon/common/crypto"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
)

// Golden vectors are the spec in executable form.
//
// Any reimplementation — in another language, by another author — validates
// against this file, and any change that moves a vector is a consensus change
// by definition. That is the point: it makes "did we change the protocol?" a
// question with a mechanical answer rather than a judgement call.
//
// The digest is sha3-256 of the encoded ZDU, not the account block hash. A
// block hash commits to the whole block — height, previous hash, plasma,
// nonce — none of which the protocol defines, so it cannot be pinned by a
// wire-format vector. The digest pins exactly what this layer decides.

type vector struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Opcode      uint64          `json:"opcode"`
	OpcodeName  string          `json:"opcodeName"`
	Object      json.RawMessage `json:"object"`
	PHash       string          `json:"pHash,omitempty"`
	THash       string          `json:"tHash,omitempty"`
	CBOR        string          `json:"cbor"`
	Digest      string          `json:"sha3_256"`
}

func cmdVectors(ctx context.Context, args []string) error {
	set := fs("vectors")
	out := set.String("out", filepath.Join("testdata", "vectors", "v1.json"), "output file")
	check := set.Bool("check", false, "verify the file matches instead of writing it")
	_ = set.Parse(args)

	vecs, err := buildVectors()
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(map[string]any{
		"protocol": "zenoglyphs",
		"prefix":   protocol.Prefix,
		"version":  protocol.Version,
		"note": "Canonical CBOR (RFC 8949 §4.2). sha3_256 is over the `cbor` bytes, " +
			"which are exactly what goes in AccountBlock.data.",
		"vectors": vecs,
	}, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')

	if *check {
		have, err := os.ReadFile(*out)
		if err != nil {
			return err
		}
		if !bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(encoded)) {
			return fmt.Errorf("%s does not match the current encoder — this is a consensus change", *out)
		}
		fmt.Printf("%d vectors match\n", len(vecs))
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*out, encoded, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %d vectors to %s\n", len(vecs), *out)
	return nil
}

func buildVectors() ([]vector, error) {
	h1 := mustHex("1111111111111111111111111111111111111111111111111111111111111111")
	h2 := mustHex("2222222222222222222222222222222222222222222222222222222222222222")
	assetID := mustHex("aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")

	attrs, err := protocol.Marshal(map[string]any{"artist": "satoshi", "edition": uint64(1)})
	if err != nil {
		return nil, err
	}
	ext, err := protocol.Marshal(map[string]any{"license": "CC0", "unknownToV1": uint64(42)})
	if err != nil {
		return nil, err
	}

	specs := []struct {
		name, desc   string
		op           protocol.Opcode
		payload      any
		pHash, tHash []byte
	}{
		{
			"mint-text-standalone",
			"A complete mint in one block: no pHash, no tHash, content inline as a byte string.",
			protocol.OpMint,
			&protocol.MintWire{
				Name: "hello", Supply: 1, Decimals: 0,
				Mime: "text/plain;charset=utf-8", Data: []byte("hello, momentum"),
			}, nil, nil,
		},
		{
			"mint-full-metadata",
			"Every optional mint field populated, including a collection reference and attributes.",
			protocol.OpMint,
			&protocol.MintWire{
				Name: "Full", Description: "every field", Collection: h1,
				Supply: 1000, Decimals: 8, Attributes: attrs,
				Mime: "image/png", Data: []byte{0x89, 'P', 'N', 'G'},
			}, nil, nil,
		},
		{
			"mint-with-extensions",
			"A mint carrying the optional-extension map at both envelope and payload level. " +
				"Every key inside ext is ignorable: an indexer that does not recognise one still " +
				"decodes the ZDU, still validates it, and still computes identical state.",
			protocol.OpMint,
			&protocol.MintWire{
				Name: "Extended", Supply: 1, Decimals: 0,
				Mime: "text/plain;charset=utf-8", Data: []byte("hi"), Ext: ext,
			}, nil, nil,
		},
		{
			"mint-composite",
			"A composite: a mint declaring two imports in slot order. Slot n is served at " +
				"/content/{id}/n, so the inscribed bytes reference an import as the relative " +
				"URL \"0\" or \"1\" and never embed a hash. The indexer records the edges and " +
				"resolves nothing: a ref to an id that does not exist is a dangling edge, not " +
				"an invalid mint.",
			protocol.OpMint,
			&protocol.MintWire{
				Name: "Composite", Supply: 1, Decimals: 0,
				Mime: "text/html;charset=utf-8",
				Data: []byte(`<script src="0"></script><img src="1">`),
				Refs: [][]byte{h1, h2},
			}, nil, nil,
		},
		{
			"mint-multipart-head",
			"The head of a multi-part chain: carries metadata, the first chunk, and both hashes.",
			protocol.OpMint,
			&protocol.MintWire{
				Name: "big", Supply: 1, Decimals: 0,
				Mime: "image/png", Data: []byte{0x89, 'P', 'N', 'G', 0x0d},
			}, h1, h2,
		},
		{
			"multi-intermediary",
			"A non-terminal chunk: a bare byte string payload plus pHash. Creates no state alone.",
			protocol.OpMulti, []byte("chunk-bytes"), h1, nil,
		},
		{
			"multi-tail",
			"The tail of a chain: a chunk with neither hash. Published first, referenced by tHash.",
			protocol.OpMulti, []byte("tail-bytes"), nil, nil,
		},
		{
			"transfer",
			"Moves 42 units. The carrier block's own amount and token standard are ignored.",
			protocol.OpTransfer,
			&protocol.TransferWire{Asset: assetID, Amount: 42}, nil, nil,
		},
		{
			"create-collection",
			"Creates a collection; the collection id is the carrier block's hash.",
			protocol.OpCreateCollection,
			&protocol.CollectionWire{
				Name: "Genesis", Description: "the first", URL: "https://zenon.network",
			}, nil, nil,
		},
		{
			"create-dispenser",
			"Escrows 10 units at 100000000 per unit, priced in ZNN.",
			protocol.OpCreateDispenser,
			&protocol.DispenserWire{
				Asset: assetID, Amount: 10, Price: 100000000,
				ZTS: mustHex("0102030405060708090a"),
			}, nil, nil,
		},
		{
			"cancel-dispenser",
			"Releases escrow back to the issuer.",
			protocol.OpCancelDispenser,
			&protocol.DispenserRefWire{Dispenser: h1}, nil, nil,
		},
		{
			"dispenser-purchase",
			"Buys from a dispenser; the carrier block is the payment and must go to the issuer.",
			protocol.OpDispenserPurchase,
			&protocol.DispenserRefWire{Dispenser: h1}, nil, nil,
		},
	}

	out := make([]vector, 0, len(specs))
	for _, s := range specs {
		payload, err := protocol.Marshal(s.payload)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.name, err)
		}
		z := &protocol.ZDU{
			Prefix: protocol.Prefix, Opcode: s.op, Version: protocol.Version,
			Data: payload, PHash: s.pHash, THash: s.tHash,
		}
		if s.name == "mint-with-extensions" {
			z.Ext = ext
		}
		raw, err := z.Encode()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.name, err)
		}
		// Every vector must survive its own decoder: a vector the reference
		// implementation cannot read back is a bug in the vector, not a spec.
		if _, err := protocol.Decode(raw); err != nil {
			return nil, fmt.Errorf("%s: encoder produced bytes the decoder rejects: %w", s.name, err)
		}

		obj, err := json.Marshal(jsonView(s.payload))
		if err != nil {
			return nil, err
		}
		out = append(out, vector{
			Name: s.name, Description: s.desc,
			Opcode: uint64(s.op), OpcodeName: s.op.String(),
			Object: obj,
			PHash:  hex.EncodeToString(s.pHash),
			THash:  hex.EncodeToString(s.tHash),
			CBOR:   hex.EncodeToString(raw),
			Digest: hex.EncodeToString(crypto.Hash(raw)),
		})
	}
	return out, nil
}

// jsonView renders a payload for human review in the vector file. Binary
// fields become hex so a reader can compare them with the CBOR by eye.
func jsonView(v any) any {
	switch t := v.(type) {
	case *protocol.MintWire:
		m := map[string]any{
			"supply": t.Supply, "decimals": t.Decimals,
			"mime": t.Mime, "data": hex.EncodeToString(t.Data),
		}
		if t.Name != "" {
			m["name"] = t.Name
		}
		if t.Description != "" {
			m["description"] = t.Description
		}
		if len(t.Collection) > 0 {
			m["collection"] = hex.EncodeToString(t.Collection)
		}
		if len(t.Attributes) > 0 {
			m["attributes"] = hex.EncodeToString(t.Attributes)
		}
		if len(t.Ext) > 0 {
			m["ext"] = hex.EncodeToString(t.Ext)
		}
		return m
	case *protocol.TransferWire:
		return map[string]any{"a": hex.EncodeToString(t.Asset), "n": t.Amount}
	case *protocol.CollectionWire:
		return map[string]any{"name": t.Name, "description": t.Description, "url": t.URL}
	case *protocol.DispenserWire:
		return map[string]any{
			"a": hex.EncodeToString(t.Asset), "n": t.Amount,
			"price": t.Price, "zts": hex.EncodeToString(t.ZTS),
		}
	case *protocol.DispenserRefWire:
		return map[string]any{"d": hex.EncodeToString(t.Dispenser)}
	case []byte:
		return hex.EncodeToString(t)
	default:
		return v
	}
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
