package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/zenon-network/go-zenon/common/types"

	"github.com/Zenoglyphs-VM/zenoglyphs/internal/identity"
	"github.com/Zenoglyphs-VM/zenoglyphs/internal/inscribe"
)

// cmdSign proves control of an address by signing a message with its key.
//
// This exists because there is nowhere else to do it. znn-cli has no
// sign-message command, and syrius only signs through WalletConnect, so a
// challenge from the Telegram bot would otherwise be unanswerable by anyone
// without a Go toolchain and a spare afternoon. It signs exactly what a
// wallet signs — the raw message bytes, no envelope (see internal/identity)
// — so a signature made here and one made by a wallet that grows the feature
// later are the same signature.
//
// The message is read from stdin rather than taken as an argument. A
// challenge is multi-line, and an argument would land in shell history: it is
// not a secret, but the habit is the same one that keeps mnemonics and
// passwords off the command line everywhere else in this tool.
func cmdSign(_ context.Context, args []string) error {
	set := fs("sign")
	var w walletFlags
	w.register(set)
	message := set.String("message", "", "message to sign (default: read from stdin)")
	verify := set.String("verify", "", "instead of signing, check a <public key>.<signature> proof against the message")
	address := set.String("address", "", "address the proof claims, for -verify (default: this key's own)")
	_ = set.Parse(args)

	msg := *message
	if msg == "" {
		read, err := readAll(os.Stdin)
		if err != nil {
			return err
		}
		msg = read
	}
	// Trailing newlines are what a heredoc or a copy-paste adds, and they
	// change the bytes being signed. Trimming them here means the obvious
	// thing works; nothing inside the message is touched.
	msg = strings.TrimRight(msg, "\r\n")
	if msg == "" {
		return fmt.Errorf("nothing to sign: pass -message or pipe the challenge in on stdin")
	}

	if *verify != "" {
		proof, err := identity.ParseProof(*verify)
		if err != nil {
			return err
		}
		addr := *address
		if addr == "" {
			addr, err = addressOfProof(proof)
			if err != nil {
				return err
			}
		}
		if err := identity.Verify(msg, addr, proof.PublicKey, proof.Signature); err != nil {
			return err
		}
		fmt.Printf("valid: %s signed this message\n", addr)
		return nil
	}

	src, err := w.source()
	if err != nil {
		return err
	}
	key, err := inscribe.LoadKey(src)
	if err != nil {
		return err
	}
	pub, sig := identity.Sign(key, msg)

	fmt.Fprintf(os.Stderr, "address: %s\n\nsend this back:\n\n", types.PubKeyToAddress(key.Public))
	// The proof itself goes to stdout, alone, so it can be piped. Everything
	// a human needs to read goes to stderr.
	fmt.Println(identity.Proof{PublicKey: pub, Signature: sig})
	return nil
}

// addressOfProof derives the address a proof's public key belongs to, which
// is what -verify checks against when no address is given.
func addressOfProof(p identity.Proof) (string, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(p.PublicKey), "0x"))
	if err != nil {
		return "", fmt.Errorf("public key is not hex: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("public key is %d bytes, want 32", len(raw))
	}
	return types.PubKeyToAddress(raw).String(), nil
}

func readAll(f *os.File) (string, error) {
	var sb strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	first := true
	for sc.Scan() {
		if !first {
			sb.WriteByte('\n')
		}
		sb.WriteString(sc.Text())
		first = false
	}
	return sb.String(), sc.Err()
}
