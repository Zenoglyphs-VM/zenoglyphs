package api

import (
	"fmt"
	"strings"
)

// Explorer is the set of block-explorer permalinks this indexer hands its
// viewer, published in /info.
//
// It is a set of URL *templates* rather than a base URL and a hard-coded path
// scheme, for two reasons that both showed up immediately:
//
//   - No two Zenon explorers agree on paths. zenonhub.io calls an account
//     block a "transaction"; the static explorer.zenon.network bundle calls it
//     the same thing but roots everything differently. A base URL plus
//     assumptions in the front end would be wrong for one of them.
//   - Not every explorer can address every object. zenonhub.io has no
//     per-momentum permalink at all — momentums are only reachable through a
//     list. An empty template says "this explorer cannot show this", and the
//     viewer renders the value as plain, copyable text instead of a dead link.
//
// Each template contains exactly one {v}, replaced with the identifier. The
// viewer does the substitution, so adding an explorer never needs a front-end
// change — only a preset here, or a -explorer URL set at the flag.
type Explorer struct {
	// Name is what the viewer shows on hover ("verify on zenonhub").
	Name string `json:"name,omitempty"`

	// Account is an address page: z1… → the account's blocks and balances.
	Account string `json:"account,omitempty"`

	// Transaction is an account block by hash. Every inscription id is one,
	// because an asset id IS the hash of the block that minted it, so this is
	// the template that carries most of the verification weight.
	Transaction string `json:"transaction,omitempty"`

	// Momentum is a momentum by height. Empty where the explorer has no such
	// page (zenonhub).
	Momentum string `json:"momentum,omitempty"`

	// Token is a ZTS page, used for the tokens a dispenser prices in.
	Token string `json:"token,omitempty"`
}

// Explorer presets, keyed by the name accepted at -explorer.
//
// The URLs were checked against the live sites rather than assumed:
// zenonhub answers /explorer/account/{addr} and redirects
// /explorer/transaction/{hash} to its canonical /explorer/block/{hash}, and
// has no momentum route in any form (/explorer/momentum/{height|hash},
// /explorer/momentums/{…} all 404). The static bundle the devnet runs is an
// Angular app whose routes are address/:address, transaction/:hash and
// momentum/:height.
var explorerPresets = map[string]Explorer{
	// Mainnet. This is the one that matters in production.
	"zenonhub": {
		Name:        "zenonhub",
		Account:     "https://zenonhub.io/explorer/account/{v}",
		Transaction: "https://zenonhub.io/explorer/transaction/{v}",
		Token:       "https://zenonhub.io/explorer/token/{v}",
		// Momentum deliberately absent — see the type comment.
	},

	// The static explorer.zenon.network bundle, which is what
	// go-zenon/docker/devnet serves on :36000. Useful before mainnet, and the
	// only preset that can deep-link a momentum.
	"devnet": {
		Name:        "devnet explorer",
		Account:     "http://localhost:36000/address/{v}",
		Transaction: "http://localhost:36000/transaction/{v}",
		Momentum:    "http://localhost:36000/momentum/{v}",
		Token:       "http://localhost:36000/token/{v}",
	},

	"none": {},
}

// ExplorerNames lists the presets, for the flag's help text.
func ExplorerNames() string { return "zenonhub, devnet, none, auto" }

// ResolveExplorer turns the -explorer flag into the templates /info publishes.
//
// "auto" is the default and picks by the chain identifier the node reported,
// so an operator who moves an indexer from devnet to mainnet does not also
// have to remember to repoint its links. A chain this does not recognise gets
// no explorer rather than a guess: a link to the wrong network's explorer is
// worse than no link, because it renders as a confident 404.
//
// Anything containing "{v}" is taken as a bare Transaction template, which is
// the escape hatch for an explorer nobody has written a preset for.
func ResolveExplorer(flag string, chainID uint64) (Explorer, error) {
	switch {
	case flag == "" || flag == "auto":
		switch chainID {
		case 1:
			return explorerPresets["zenonhub"], nil
		case 69:
			return explorerPresets["devnet"], nil
		default:
			// Includes chainID 0, which is "the node has not answered yet".
			// serve re-resolves once it has.
			return Explorer{}, nil
		}
	case strings.Contains(flag, "{v}"):
		return Explorer{Name: "explorer", Transaction: flag}, nil
	default:
		e, ok := explorerPresets[flag]
		if !ok {
			return Explorer{}, fmt.Errorf("unknown explorer %q (want one of: %s, or a URL template containing {v})", flag, ExplorerNames())
		}
		return e, nil
	}
}
