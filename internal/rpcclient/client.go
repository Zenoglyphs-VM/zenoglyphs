// Package rpcclient is a small JSON-RPC client for a NoM node.
//
// It deliberately does not import go-zenon's rpc/api package: that would pull
// the whole chain implementation in for the sake of a handful of struct tags.
// The types here mirror the node's JSON shapes and nothing else.
package rpcclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/types"
)

// MaxCountSize mirrors rpc/api.RpcMaxCountSize — the node rejects a larger
// count outright.
const MaxCountSize = 1024

type Client struct {
	url  string
	http *http.Client
	id   atomic.Uint64
}

func New(url string) *Client {
	return &Client{
		url:  url,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, params []any, out any) error {
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.id.Add(1),
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: http %d: %s", method, resp.StatusCode, truncate(raw, 200))
	}
	var r rpcResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("%s: bad json: %w", method, err)
	}
	if r.Error != nil {
		return fmt.Errorf("%s: %w", method, r.Error)
	}
	if out == nil {
		return nil
	}
	if len(r.Result) == 0 || string(r.Result) == "null" {
		return fmt.Errorf("%s: null result", method)
	}
	return json.Unmarshal(r.Result, out)
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// --- ledger types ---

// AccountHeader is one entry of momentum.content. Its position in that list
// is the second half of the canonical order, so the slice order the node
// returns is load-bearing and must never be re-sorted client side.
type AccountHeader struct {
	Address types.Address `json:"address"`
	Hash    types.Hash    `json:"hash"`
	Height  uint64        `json:"height"`
}

type Momentum struct {
	Version         uint64 `json:"version"`
	ChainIdentifier uint64 `json:"chainIdentifier"`

	Hash         types.Hash      `json:"hash"`
	PreviousHash types.Hash      `json:"previousHash"`
	Height       uint64          `json:"height"`
	Timestamp    int64           `json:"timestamp"`
	Content      []AccountHeader `json:"content"`
	Producer     types.Address   `json:"producer"`

	// The dynamic-plasma resource prices this momentum sets for the next one,
	// in units of protocol.PriceScaleFactor. Absent before the spork and on a
	// pre-v2 momentum, where they decode to zero and every caller reads that
	// as the floor.
	NextFusionPrice uint64 `json:"nextFusionPrice"`
	NextWorkPrice   uint64 `json:"nextWorkPrice"`
}

type ConfirmationDetail struct {
	NumConfirmations uint64     `json:"numConfirmations"`
	MomentumHeight   uint64     `json:"momentumHeight"`
	MomentumHash     types.Hash `json:"momentumHash"`
}

type AccountBlock struct {
	BlockType     uint64                   `json:"blockType"`
	Hash          types.Hash               `json:"hash"`
	PreviousHash  types.Hash               `json:"previousHash"`
	Height        uint64                   `json:"height"`
	Address       types.Address            `json:"address"`
	ToAddress     types.Address            `json:"toAddress"`
	Amount        BigInt                   `json:"amount"`
	TokenStandard types.ZenonTokenStandard `json:"tokenStandard"`
	FromBlockHash types.Hash               `json:"fromBlockHash"`
	Data          []byte                   `json:"data"`

	// What the block paid for its bandwidth, under dynamic plasma.
	//
	// FusedPlasma and Difficulty are both committed to by the block hash
	// (chain/nom/account_block.go), so they are chain facts every deriver
	// sees identically. BasePlasma is *not* in the hash — it is the node's
	// own recomputation — so it is read for cross-checking and never used as
	// the unit of account; PlasmaForDataLength derives that locally.
	//
	// All three are absent from a pre-spork node's JSON and decode to zero,
	// which reads as "no dynamic-plasma information" everywhere downstream.
	FusedPlasma uint64 `json:"fusedPlasma"`
	Difficulty  uint64 `json:"difficulty"`
	BasePlasma  uint64 `json:"basePlasma"`

	ConfirmationDetail *ConfirmationDetail `json:"confirmationDetail"`
}

// DetailedMomentum pairs a momentum with the full bodies of its blocks. It is
// what makes indexing viable: one request per batch of momentums instead of
// one getAccountBlockByHash per block, which cannot keep up with the tip.
type DetailedMomentum struct {
	AccountBlocks []*AccountBlock `json:"blocks"`
	Momentum      *Momentum       `json:"momentum"`
}

type detailedMomentumList struct {
	List  []*DetailedMomentum `json:"list"`
	Count int                 `json:"count"`
}

// BigInt decodes a NoM amount, which the node renders as a JSON *string* to
// keep values above 2^53 intact through JavaScript clients. A plain *big.Int
// field cannot read that, and the failure is total — the whole momentum fails
// to parse — so the tolerance has to live in the type.
type BigInt struct{ *big.Int }

func (b *BigInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		b.Int = big.NewInt(0)
		return nil
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return fmt.Errorf("amount %q is not a decimal integer", s)
	}
	b.Int = v
	return nil
}

func (b BigInt) String() string {
	if b.Int == nil {
		return "0"
	}
	return b.Int.String()
}

type accountInfo struct {
	Address        types.Address `json:"address"`
	BlockCount     uint64        `json:"blockCount"`
	BalanceInfoMap map[string]struct {
		Balance BigInt `json:"balance"`
	} `json:"balanceInfoMap"`
}

// --- ledger calls ---

func (c *Client) FrontierMomentum(ctx context.Context) (*Momentum, error) {
	var m Momentum
	return &m, c.call(ctx, "ledger.getFrontierMomentum", nil, &m)
}

// DetailedMomentumsByHeight fetches count momentums starting at height, each
// with its account blocks.
func (c *Client) DetailedMomentumsByHeight(ctx context.Context, height, count uint64) ([]*DetailedMomentum, error) {
	if count > MaxCountSize {
		count = MaxCountSize
	}
	var list detailedMomentumList
	if err := c.call(ctx, "ledger.getDetailedMomentumsByHeight", []any{height, count}, &list); err != nil {
		return nil, err
	}
	return list.List, nil
}

func (c *Client) AccountBlockByHash(ctx context.Context, hash types.Hash) (*AccountBlock, error) {
	var b AccountBlock
	if err := c.call(ctx, "ledger.getAccountBlockByHash", []any{hash.String()}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func (c *Client) FrontierAccountBlock(ctx context.Context, addr types.Address) (*AccountBlock, error) {
	var b AccountBlock
	err := c.call(ctx, "ledger.getFrontierAccountBlock", []any{addr.String()}, &b)
	if err != nil {
		// An address that has never sent has no frontier; that is not a failure.
		return nil, nil //nolint:nilerr // absence is the expected signal here
	}
	return &b, nil
}

// Balance returns the account's balance for one token standard.
func (c *Client) Balance(ctx context.Context, addr types.Address, zts types.ZenonTokenStandard) (*big.Int, error) {
	var info accountInfo
	if err := c.call(ctx, "ledger.getAccountInfoByAddress", []any{addr.String()}, &info); err != nil {
		return nil, err
	}
	if e, ok := info.BalanceInfoMap[zts.String()]; ok && e.Balance.Int != nil {
		return e.Balance.Int, nil
	}
	return big.NewInt(0), nil
}

func (c *Client) PublishRawTransaction(ctx context.Context, block *nom.AccountBlock) error {
	return c.call(ctx, "ledger.publishRawTransaction", []any{block}, nil)
}

// UnreceivedBlocks lists sends addressed to an account that it has not
// received yet.
//
// On NoM a send is final the moment it confirms, but the value only lands in
// the recipient's balance once the recipient publishes a matching receive
// block. Until then the send sits here — which makes this both the inbox and
// the record of who paid what, and means an account can decline to take
// delivery of something simply by not receiving it.
func (c *Client) UnreceivedBlocks(ctx context.Context, addr types.Address, page, size uint32) ([]*AccountBlock, error) {
	var list struct {
		List  []*AccountBlock `json:"list"`
		Count int             `json:"count"`
	}
	if err := c.call(ctx, "ledger.getUnreceivedBlocksByAddress",
		[]any{addr.String(), page, size}, &list); err != nil {
		return nil, err
	}
	return list.List, nil
}

// Token is what the node knows about one token standard. Only the fields a
// client needs to render an amount are decoded: an amount on the wire is in
// the smallest unit, and printing it without the decimals is how a 1000 PP
// price becomes an unreadable number.
type Token struct {
	Name          string                   `json:"name"`
	Symbol        string                   `json:"symbol"`
	Decimals      uint8                    `json:"decimals"`
	TokenStandard types.ZenonTokenStandard `json:"tokenStandard"`
}

func (c *Client) TokenByZTS(ctx context.Context, zts types.ZenonTokenStandard) (*Token, error) {
	var t Token
	if err := c.call(ctx, "embedded.token.getByZts", []any{zts.String()}, &t); err != nil {
		return nil, err
	}
	if t.Symbol == "" {
		return nil, fmt.Errorf("no token registered for %s", zts)
	}
	return &t, nil
}

// --- plasma ---

// RequiredPlasma is what the node says a block will cost, including the PoW
// difficulty to target if the account has no fused plasma.
//
// Asking the node rather than recomputing the formula locally is deliberate:
// under dynamic plasma the price moves per momentum, so any local copy of the
// calculation is a copy that can be wrong at exactly the wrong moment.
type RequiredPlasma struct {
	AvailablePlasma    uint64 `json:"availablePlasma"`
	BasePlasma         uint64 `json:"basePlasma"`
	RequiredDifficulty uint64 `json:"requiredDifficulty"`
}

// PlasmaInfo is an account's fused plasma. MaxPlasma is what its fusions
// entitle it to; CurrentPlasma is that less whatever its uncommitted blocks
// are holding.
type PlasmaInfo struct {
	CurrentPlasma uint64 `json:"currentPlasma"`
	MaxPlasma     uint64 `json:"maxPlasma"`
	QsrAmount     BigInt `json:"qsrAmount"`
}

func (c *Client) Plasma(ctx context.Context, addr types.Address) (*PlasmaInfo, error) {
	var p PlasmaInfo
	return &p, c.call(ctx, "embedded.plasma.get", []any{addr.String()}, &p)
}

func (c *Client) RequiredPoW(ctx context.Context, from, to types.Address, blockType uint64, data []byte) (*RequiredPlasma, error) {
	var r RequiredPlasma
	param := map[string]any{
		"address":   from.String(),
		"blockType": blockType,
		"toAddress": to.String(),
		"data":      data, // marshals to base64, which is what the node expects
	}
	return &r, c.call(ctx, "embedded.plasma.getRequiredPoWForAccountBlock", []any{param}, &r)
}
