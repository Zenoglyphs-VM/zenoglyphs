package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/inscribe"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/protocol"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/rpcclient"
)

// devnetMnemonic is the phrase committed to go-zenon's docker/devnet. It is
// public by design and worthless outside chain 69 — it is here so that
// `-devnet` works with no setup, and it must never be the default for any
// other network.
const devnetMnemonic = "abstract affair idle position alien fluid board ordinary exist afraid chapter wood wood guide sun walnut crew perfect place firm poverty model side million"

// walletFlags is the key-source flag set shared by every publishing command.
type walletFlags struct {
	node         string
	mnemonicFile string
	mnemonicEnv  string
	keystore     string
	password     string
	index        uint
	devnet       bool
	verbose      bool
}

func (w *walletFlags) register(set *flag.FlagSet) {
	set.StringVar(&w.node, "node", defaultNode, "NoM node JSON-RPC endpoint")
	set.StringVar(&w.mnemonicFile, "mnemonic-file", "", "file holding a BIP-39 phrase")
	set.StringVar(&w.mnemonicEnv, "mnemonic-env", "", "environment variable holding a BIP-39 phrase")
	set.StringVar(&w.keystore, "keystore", "", "encrypted go-zenon keystore file")
	set.StringVar(&w.password, "password", "", "keystore password (prefer ZCRIBE_PASSWORD)")
	set.UintVar(&w.index, "index", 1, "account index under m/44'/73404'/i'")
	set.BoolVar(&w.devnet, "devnet", false, "use the public devnet mnemonic (chain 69 only)")
	set.BoolVar(&w.verbose, "v", false, "debug logging")
}

// A phrase is never accepted as a command-line argument: arguments land in
// shell history and in the process table, where any local user can read them.
func (w *walletFlags) source() (inscribe.KeySource, error) {
	src := inscribe.KeySource{Index: uint32(w.index)}
	switch {
	case w.devnet:
		src.Mnemonic = devnetMnemonic
	case w.mnemonicFile != "":
		m, err := inscribe.ReadMnemonicFile(w.mnemonicFile)
		if err != nil {
			return src, err
		}
		src.Mnemonic = m
	case w.mnemonicEnv != "":
		m := os.Getenv(w.mnemonicEnv)
		if m == "" {
			return src, fmt.Errorf("environment variable %s is empty", w.mnemonicEnv)
		}
		src.Mnemonic = m
	case w.keystore != "":
		src.KeyFilePath = w.keystore
		src.Password = w.password
		if env := os.Getenv("ZCRIBE_PASSWORD"); env != "" {
			src.Password = env
		}
	default:
		return src, fmt.Errorf("no key source: pass -devnet, -mnemonic-file, -mnemonic-env, or -keystore")
	}
	return src, nil
}

func (w *walletFlags) inscriber(ctx context.Context) (*inscribe.Inscriber, error) {
	src, err := w.source()
	if err != nil {
		return nil, err
	}
	key, err := inscribe.LoadKey(src)
	if err != nil {
		return nil, err
	}
	client := rpcclient.New(w.node)
	id, err := chainID(ctx, client)
	if err != nil {
		return nil, err
	}
	return inscribe.New(client, key, id, newLogger(w.verbose))
}

func cmdAddress(ctx context.Context, args []string) error {
	set := fs("address")
	var w walletFlags
	w.register(set)
	_ = set.Parse(args)

	src, err := w.source()
	if err != nil {
		return err
	}
	key, err := inscribe.LoadKey(src)
	if err != nil {
		return err
	}
	fmt.Println(types.PubKeyToAddress(key.Public).String())
	return nil
}

func cmdInscribe(ctx context.Context, args []string) error {
	set := fs("inscribe")
	var w walletFlags
	w.register(set)
	file := set.String("file", "", "file to inscribe (required)")
	name := set.String("name", "", "asset name")
	desc := set.String("description", "", "asset description")
	mime := set.String("mime", "", "content type (defaults to a guess from the file extension)")
	supply := set.Uint64("supply", 1, "units to issue")
	decimals := set.Uint64("decimals", 0, "decimal places")
	collection := set.String("collection", "", "collection id to join (must be one you issued)")
	to := set.String("to", "", "address that receives the supply (defaults to the issuer)")
	pow := set.Bool("pow", false, "size blocks for proof-of-work plasma (1080 bytes) instead of fused (16 KB)")
	ext := set.String("ext", "", `optional extensions as a JSON object, e.g. '{"license":"CC0"}' — carried and served, never interpreted`)
	refs := set.String("refs", "", "comma-separated asset ids this inscription imports, in slot order (composites, spec R-4.7)")
	_ = set.Parse(args)

	if *file == "" {
		return fmt.Errorf("-file is required")
	}
	content, err := os.ReadFile(*file)
	if err != nil {
		return err
	}

	mimeType := *mime
	if mimeType == "" {
		guess, ok := protocol.MimeForFile(*file)
		if !ok {
			return fmt.Errorf("cannot infer a permitted mime type from %q; pass -mime\nallowed: %s",
				filepath.Ext(*file), strings.Join(protocol.AllowedMimes(), ", "))
		}
		mimeType = guess
	}
	if !protocol.MimeAllowed(mimeType) {
		return fmt.Errorf("mime %q is not in the allowlist:\n  %s",
			mimeType, strings.Join(protocol.AllowedMimes(), "\n  "))
	}

	assetName := *name
	if assetName == "" {
		assetName = filepath.Base(*file)
	}

	meta := protocol.MintWire{
		Name: assetName, Description: *desc,
		Supply: *supply, Decimals: *decimals, Mime: mimeType,
	}
	if *collection != "" {
		raw, err := hex.DecodeString(*collection)
		if err != nil || len(raw) != protocol.HashLen {
			return fmt.Errorf("collection id must be 64 hex characters")
		}
		meta.Collection = raw
	}
	if *ext != "" {
		enc, err := extToCBOR(*ext)
		if err != nil {
			return err
		}
		meta.Ext = enc
	}
	if *refs != "" {
		parsed, err := parseRefs(*refs)
		if err != nil {
			return err
		}
		meta.Refs = parsed
	}
	if err := meta.Validate(); err != nil {
		return err
	}

	in, err := w.inscriber(ctx)
	if err != nil {
		return err
	}
	recipient := in.Address()
	if *to != "" {
		recipient, err = types.ParseAddress(*to)
		if err != nil {
			return fmt.Errorf("bad -to address: %w", err)
		}
	}

	mode := protocol.PlasmaFused
	if *pow {
		mode = protocol.PlasmaPoW
	}

	hash, err := in.InscribeFile(ctx, meta, content, mode, recipient)
	if err != nil {
		return err
	}
	fmt.Printf("inscribed %s\n  asset id: %s\n  issuer:   %s\n  owner:    %s\n",
		*file, hash, in.Address(), recipient)
	if n := len(meta.Refs); n > 0 {
		fmt.Printf("  imports:  %d slot(s); reachable from the inscribed bytes as", n)
		for i := 0; i < n; i++ {
			fmt.Printf(" \"%d\"", i)
		}
		fmt.Println()
	}
	return nil
}

// parseRefs turns a comma-separated id list into the wire form.
//
// Order is the whole meaning of the list — slot n is the nth entry, and the
// inscribed bytes reach it as the relative URL "n" — so the list is taken
// exactly as written, never sorted or deduplicated.
func parseRefs(list string) ([][]byte, error) {
	parts := strings.Split(list, ",")
	out := make([][]byte, 0, len(parts))
	for i, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		raw, err := hex.DecodeString(p)
		if err != nil || len(raw) != protocol.HashLen {
			return nil, fmt.Errorf("ref %d (%q) must be 64 hex characters", i, p)
		}
		out = append(out, raw)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-refs was given but names no ids")
	}
	if len(out) > protocol.MaxRefs {
		return nil, fmt.Errorf("%d imports exceeds the %d-slot limit", len(out), protocol.MaxRefs)
	}
	return out, nil
}

// extToCBOR converts a JSON object from the command line into the canonical
// CBOR the wire carries. It goes through JSON only because that is what a
// human can type; the protocol never sees JSON.
//
// The round trip is deliberately lossy in one direction: JSON has no byte
// strings, so anything binary has to be encoded by the author (hex, base64)
// and stays a text string on the wire. ext is for annotations, not payloads —
// R-2.9.2 caps it at 2 KB precisely so it cannot become a second data
// channel.
func extToCBOR(jsonObj string) ([]byte, error) {
	var v map[string]any
	if err := json.Unmarshal([]byte(jsonObj), &v); err != nil {
		return nil, fmt.Errorf("-ext must be a JSON object: %w", err)
	}
	enc, err := protocol.Marshal(v)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidateExt(enc); err != nil {
		return nil, err
	}
	return enc, nil
}

func cmdTransfer(ctx context.Context, args []string) error {
	set := fs("transfer")
	var w walletFlags
	w.register(set)
	asset := set.String("asset", "", "asset id (64 hex characters)")
	amount := set.Uint64("amount", 1, "units to move")
	to := set.String("to", "", "recipient address")
	burn := set.Bool("burn", false, "send to the burn address instead of -to")
	_ = set.Parse(args)

	id, err := inscribe.HexHash(*asset)
	if err != nil {
		return fmt.Errorf("-asset: %w", err)
	}

	in, err := w.inscriber(ctx)
	if err != nil {
		return err
	}

	recipient := protocol.BurnAddress
	if !*burn {
		if *to == "" {
			return fmt.Errorf("-to is required unless -burn is set")
		}
		recipient, err = types.ParseAddress(*to)
		if err != nil {
			return fmt.Errorf("bad -to address: %w", err)
		}
	}

	hash, err := in.Transfer(ctx, id, *amount, recipient)
	if err != nil {
		return err
	}
	verb := "transferred"
	if *burn {
		verb = "burned"
	}
	fmt.Printf("%s %d units of %s\n  block: %s\n  to:    %s\n", verb, *amount, *asset, hash, recipient)
	return nil
}

func cmdCollection(ctx context.Context, args []string) error {
	set := fs("collection")
	var w walletFlags
	w.register(set)
	name := set.String("name", "", "collection name (required)")
	desc := set.String("description", "", "collection description")
	url := set.String("url", "", "collection url")
	_ = set.Parse(args)

	if *name == "" {
		return fmt.Errorf("-name is required")
	}
	in, err := w.inscriber(ctx)
	if err != nil {
		return err
	}
	hash, err := in.CreateCollection(ctx, protocol.CollectionWire{
		Name: *name, Description: *desc, URL: *url,
	})
	if err != nil {
		return err
	}
	fmt.Printf("created collection %q\n  collection id: %s\n  issuer:        %s\n", *name, hash, in.Address())
	return nil
}

func cmdDispenser(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: zcribe dispenser <create|cancel|buy> [flags]")
	}
	sub, rest := args[0], args[1:]

	set := fs("dispenser " + sub)
	var w walletFlags
	w.register(set)
	asset := set.String("asset", "", "asset id (create)")
	amount := set.Uint64("amount", 1, "units offered (create)")
	price := set.Uint64("price", 1, "price per unit in the smallest unit of -zts (create)")
	ztsFlag := set.String("zts", types.ZnnTokenStandard.String(), "payment token standard (create)")
	id := set.String("id", "", "dispenser id (cancel, buy)")
	seller := set.String("seller", "", "dispenser issuer's address (buy)")
	pay := set.String("pay", "", "total to pay, in the smallest unit (buy)")
	_ = set.Parse(rest)

	in, err := w.inscriber(ctx)
	if err != nil {
		return err
	}

	switch sub {
	case "create":
		assetID, err := inscribe.HexHash(*asset)
		if err != nil {
			return fmt.Errorf("-asset: %w", err)
		}
		zts, err := types.ParseZTS(*ztsFlag)
		if err != nil {
			return fmt.Errorf("-zts: %w", err)
		}
		hash, err := in.CreateDispenser(ctx, protocol.DispenserWire{
			Asset: assetID.Bytes(), Amount: *amount, Price: *price, ZTS: zts.Bytes(),
		})
		if err != nil {
			return err
		}
		fmt.Printf("dispenser open\n  dispenser id: %s\n  offering:     %d units at %d %s each\n",
			hash, *amount, *price, zts)
		return nil

	case "cancel":
		dispID, err := inscribe.HexHash(*id)
		if err != nil {
			return fmt.Errorf("-id: %w", err)
		}
		hash, err := in.CancelDispenser(ctx, dispID)
		if err != nil {
			return err
		}
		fmt.Printf("cancelled dispenser %s\n  block: %s\n", *id, hash)
		return nil

	case "buy":
		dispID, err := inscribe.HexHash(*id)
		if err != nil {
			return fmt.Errorf("-id: %w", err)
		}
		sellerAddr, err := types.ParseAddress(*seller)
		if err != nil {
			return fmt.Errorf("-seller: %w", err)
		}
		total, ok := new(big.Int).SetString(*pay, 10)
		if !ok {
			return fmt.Errorf("-pay must be a decimal amount")
		}
		zts, err := types.ParseZTS(*ztsFlag)
		if err != nil {
			return fmt.Errorf("-zts: %w", err)
		}
		// The carrier block is the payment, so it has to go to the seller.
		// A purchase paid to anyone else is rejected by every indexer.
		hash, err := in.Purchase(ctx, dispID, sellerAddr, total, zts)
		if err != nil {
			return err
		}
		fmt.Printf("purchase sent\n  block: %s\n  paid:  %s %s to %s\n", hash, total, zts, sellerAddr)
		return nil

	default:
		return fmt.Errorf("unknown subcommand %q: expected create, cancel, or buy", sub)
	}
}
