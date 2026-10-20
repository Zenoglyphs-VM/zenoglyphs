package evm

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Reading a transaction back.
//
// abi.go builds calldata. This reads it, which is a different job with a
// different failure mode: encoding a bad argument produces an error the caller
// sees immediately, while decoding runs against bytes an explorer did not
// author and cannot trust. So everything here is total — it returns what it
// could read and says where it stopped, and never panics on a truncated word,
// a nonsense offset, or a length prefix claiming four gigabytes.
//
// What it is for: `a9059cbb0000…0a60000…900000` is not something anyone can
// read. `transfer(to: z1qzx2uq…, value: 2500 GLYPH)` is.

// Param is one decoded ABI value.
type Param struct {
	Name string `json:"name,omitempty"` // from a known signature, else empty
	Type string `json:"type"`
	// Value is the canonical rendering for the type: a decimal string for
	// integers, 0x-hex for bytes, true/false for bool, hex for an address.
	// A string is always a string, never a number, because a uint256 does not
	// survive JSON's number type.
	Value string `json:"value"`
	// Zenon is the native spelling of an address parameter — for every
	// address, contracts included. Whether it is one a wallet may pay is a
	// separate question, answered by Sendable rather than by withholding the
	// name; see Address.Display.
	Zenon string `json:"zenon,omitempty"`
	// Sendable is false for an address no keypair can reach. Set only on
	// address-typed parameters, where it is the difference between a
	// recipient and a contract handle.
	Sendable bool `json:"sendable,omitempty"`
	// Items is set for an array type, and holds its elements.
	Items []Param `json:"items,omitempty"`

	// Hex is the raw 32-byte word, set only when the type is unknown and the
	// reader is being shown alternative readings of the same bytes rather
	// than one decoded value. See rawWords.
	Hex string `json:"hex,omitempty"`
}

// Call is a decoded call: which function, and with what.
type Call struct {
	Selector  string  `json:"selector"`
	Signature string  `json:"signature,omitempty"`
	Name      string  `json:"name,omitempty"`
	Params    []Param `json:"params,omitempty"`
	// Note explains anything the decode could not do, so a reader is never
	// left guessing whether an empty list means "no arguments" or "gave up".
	Note string `json:"note,omitempty"`
}

// signatureParams splits "transfer(address,uint256)" into its name and its
// parameter types. Nested tuples are not split — this ABI has no tuples — so a
// signature containing one is reported rather than mis-parsed.
func signatureParams(sig string) (name string, types []string, err error) {
	open := strings.IndexByte(sig, '(')
	if open < 0 || !strings.HasSuffix(sig, ")") {
		return "", nil, fmt.Errorf("evm: %q is not a function signature", sig)
	}
	name = sig[:open]
	body := sig[open+1 : len(sig)-1]
	if body == "" {
		return name, nil, nil
	}
	if strings.ContainsAny(body, "()") {
		return name, nil, fmt.Errorf("evm: tuple parameters are not supported: %q", sig)
	}
	for _, t := range strings.Split(body, ",") {
		types = append(types, strings.TrimSpace(t))
	}
	return name, types, nil
}

// isDynamic reports whether a type is encoded out-of-line, as an offset into
// the tail rather than as a value in the head.
func isDynamic(t string) bool {
	return t == "string" || t == "bytes" || strings.HasSuffix(t, "[]")
}

// DecodeParams reads an ABI-encoded argument list.
//
// data is the encoded region *without* any selector — the caller strips that,
// because the same function decodes return values, which have none.
//
// Nothing here returns an error. An explorer rendering a page is not in a
// position to refuse: the calldata is on the chain either way, and a page that
// 500s because one argument is malformed is less useful than one that shows
// three arguments and a note. Anything unreadable comes back as its raw word.
func DecodeParams(types []string, data []byte) []Param {
	out := make([]Param, 0, len(types))
	for i, t := range types {
		head := i * 32
		if head+32 > len(data) {
			out = append(out, Param{Type: t, Value: "", Items: nil})
			continue
		}
		word := data[head : head+32]
		if !isDynamic(t) {
			out = append(out, decodeStatic(t, word))
			continue
		}
		off := offsetOf(word, len(data))
		if off < 0 {
			out = append(out, Param{Type: t, Value: "0x" + hex.EncodeToString(word)})
			continue
		}
		out = append(out, decodeDynamic(t, data, off))
	}
	return out
}

// offsetOf reads a tail offset and rejects anything that does not point inside
// the data. A hostile or truncated encoding is the normal case here, not the
// exceptional one.
func offsetOf(word []byte, size int) int {
	n := new(big.Int).SetBytes(word)
	if !n.IsInt64() {
		return -1
	}
	off := int(n.Int64())
	if off < 0 || off+32 > size {
		return -1
	}
	return off
}

func decodeStatic(t string, word []byte) Param {
	p := Param{Type: t}
	switch {
	case t == "address":
		var a Address
		copy(a[:], word[12:])
		p.Value = a.Hex()
		p.Zenon, p.Sendable = a.Zenon(), a.Sendable()
	case t == "bool":
		p.Value = "false"
		if new(big.Int).SetBytes(word).Sign() != 0 {
			p.Value = "true"
		}
	case strings.HasPrefix(t, "uint"):
		p.Value = new(big.Int).SetBytes(word).String()
	case strings.HasPrefix(t, "int"):
		p.Value = signed(new(big.Int).SetBytes(word)).String()
	case strings.HasPrefix(t, "bytes"):
		// bytesN is left-aligned: the value is the first N bytes, and the rest
		// is padding that must not be shown as data.
		n, err := strconv.Atoi(strings.TrimPrefix(t, "bytes"))
		if err != nil || n <= 0 || n > 32 {
			n = 32
		}
		p.Value = "0x" + hex.EncodeToString(word[:n])
	default:
		p.Value = "0x" + hex.EncodeToString(word)
		p.Type = t + " (unrecognised)"
	}
	return p
}

// decodeDynamic reads a string, a bytes, or an array of static elements out of
// the tail at off.
func decodeDynamic(t string, data []byte, off int) Param {
	p := Param{Type: t}
	length := new(big.Int).SetBytes(data[off : off+32])
	if !length.IsInt64() {
		p.Value = "(length out of range)"
		return p
	}
	n := int(length.Int64())
	body := data[off+32:]

	if elem, ok := strings.CutSuffix(t, "[]"); ok {
		if isDynamic(elem) {
			p.Value = fmt.Sprintf("(%d items; arrays of %s are not decoded)", n, elem)
			return p
		}
		// Cap what is materialised: the length prefix is attacker-controlled
		// and 2^64 items would be an allocation, not a decode.
		if n*32 > len(body) {
			n = len(body) / 32
		}
		for i := 0; i < n; i++ {
			p.Items = append(p.Items, decodeStatic(elem, body[i*32:i*32+32]))
		}
		p.Value = fmt.Sprintf("%d item%s", len(p.Items), plural(len(p.Items)))
		return p
	}

	if n > len(body) {
		n = len(body)

	}
	raw := body[:n]
	if t == "string" {
		if utf8.Valid(raw) {
			p.Value = string(raw)
		} else {
			// Not text. Showing it as text would render mojibake and imply the
			// contract stored something it did not.
			p.Value = "0x" + hex.EncodeToString(raw)
			p.Type = "string (not valid UTF-8)"
		}
		return p
	}
	p.Value = "0x" + hex.EncodeToString(raw)
	return p
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// DecodeCall decodes calldata against a known signature.
//
// sig is what the caller resolved the four-byte selector to — from a registry,
// or from a contract's own ABI. An empty sig means the selector is unknown,
// and the honest rendering of that is the selector plus the raw argument words,
// which is still better than one long hex string: a reader can at least see
// how many arguments there were and recognise an address among them.
func DecodeCall(sig string, calldata []byte) Call {
	c := Call{}
	if len(calldata) < 4 {
		if len(calldata) > 0 {
			c.Note = fmt.Sprintf("%d bytes, too short to carry a selector", len(calldata))
		}
		return c
	}
	c.Selector = "0x" + hex.EncodeToString(calldata[:4])
	args := calldata[4:]

	if sig == "" {
		c.Note = "unknown selector: no signature is registered for it, so each argument is shown as a raw word with every reading the bytes allow"
		c.Params = rawWords(args)
		return c
	}
	c.Signature = sig
	name, types, err := signatureParams(sig)
	if err != nil {
		c.Note = err.Error()
		c.Params = rawWords(args)
		return c
	}
	c.Name = name
	c.Params = DecodeParams(types, args)
	if err := checkArgLength(types, args); err != "" {
		c.Note = err
	}
	return c
}

// checkArgLength reports a mismatch between the signature and the calldata,
// which is worth saying out loud: it means the selector matched but the
// arguments did not, so either the registry is wrong or the call was malformed
// — and in both cases the decoded values below it should not be trusted.
func checkArgLength(types []string, args []byte) string {
	want := len(types) * 32
	switch {
	case len(args) < want:
		return fmt.Sprintf("calldata is %d bytes short of what %d argument%s need",
			want-len(args), len(types), plural(len(types)))
	case len(args) > want && !anyDynamic(types):
		return fmt.Sprintf("%d trailing bytes after the last argument", len(args)-want)
	}
	return ""
}

func anyDynamic(types []string) bool {
	for _, t := range types {
		if isDynamic(t) {
			return true
		}
	}
	return false
}

// rawWords is the fallback for calldata whose selector nobody registered.
//
// A 32-byte word does not say what it is. `0x…2a` is 42, and it is also an
// address whose last byte is 0x2a, and the encoding is identical — which
// interpretation is right depends on a signature that, by definition, is not
// available here. An earlier version guessed "address if the top twelve bytes
// are zero" and confidently labelled the integer 42 an address.
//
// So this guesses nothing. Each word is given as a number, as raw bytes, and —
// when the shape permits it — as an address, and the reader decides. Offering
// three readings of one word is less tidy than picking one and is the only
// honest thing to do without the ABI.
func rawWords(args []byte) []Param {
	var out []Param
	for i := 0; i+32 <= len(args); i += 32 {
		w := args[i : i+32]
		p := Param{
			Type:  "word",
			Value: new(big.Int).SetBytes(w).String(),
			Hex:   "0x" + hex.EncodeToString(w),
		}
		// Address-shaped means the top twelve bytes are zero, which is how an
		// ABI-encoded address sits in a word. Every small integer is also
		// address-shaped, which is exactly why this is an extra reading and
		// not the type.
		if isZeroPrefix(w[:12]) {
			var a Address
			copy(a[:], w[12:])
			p.Zenon, p.Sendable = a.Zenon(), a.Sendable()
		}
		out = append(out, p)
	}
	if rem := len(args) % 32; rem != 0 {
		out = append(out, Param{
			Type: "trailing", Hex: "0x" + hex.EncodeToString(args[len(args)-rem:]),
			Value: fmt.Sprintf("%d bytes that do not fill a word", rem),
		})
	}
	return out
}

func isZeroPrefix(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// DecodeEvent decodes a log against a known event signature, pairing indexed
// parameters with topics and the rest with the data blob.
//
// The split is the part worth getting right and the part every naive decoder
// gets wrong: `Transfer(address,address,uint256)` puts the two addresses in
// topics[1] and topics[2] and the amount in data, so decoding all three from
// data yields three wrong answers.
func DecodeEvent(sig string, indexed []bool, topics [][]byte, data []byte) Call {
	c := Call{}
	if len(topics) > 0 {
		c.Selector = "0x" + hex.EncodeToString(topics[0])
	}
	if sig == "" {
		c.Note = "unknown event signature"
		return c
	}
	c.Signature = sig
	name, types, err := signatureParams(sig)
	if err != nil {
		c.Note = err.Error()
		return c
	}
	c.Name = name

	// An event whose indexed layout was not supplied: assume the leading
	// parameters are indexed, one per topic after the signature hash, which is
	// what Solidity produces for every event in this codebase's registry.
	if indexed == nil {
		indexed = make([]bool, len(types))
		for i := range indexed {
			indexed[i] = i < len(topics)-1
		}
	}

	topicAt, dataTypes, dataIdx := 1, []string(nil), []int(nil)
	params := make([]Param, len(types))
	for i, t := range types {
		if i < len(indexed) && indexed[i] {
			if topicAt < len(topics) && len(topics[topicAt]) == 32 {
				params[i] = decodeStatic(t, topics[topicAt])
				// A dynamic type cannot fit a topic, so what is stored is
				// keccak of the value. Saying so is the difference between a
				// hash and a wrong answer.
				if isDynamic(t) {
					params[i] = Param{Type: t + " (indexed: keccak of the value)",
						Value: "0x" + hex.EncodeToString(topics[topicAt])}
				}
				topicAt++
			} else {
				params[i] = Param{Type: t, Value: "(topic missing)"}
			}
			continue
		}
		dataTypes = append(dataTypes, t)
		dataIdx = append(dataIdx, i)
	}
	for j, p := range DecodeParams(dataTypes, data) {
		params[dataIdx[j]] = p
	}
	c.Params = params
	return c
}

// NameParams attaches parameter names to a decoded call.
//
// Names do not appear in a selector's preimage — `transfer(address,uint256)` is
// the whole signature, and `to` and `value` are not part of it — so they can
// only come from a registry. Applied separately for exactly that reason: the
// types are derived from the chain, the names are a label this explorer adds.
func NameParams(c *Call, names []string) {
	for i := range c.Params {
		if i < len(names) {
			c.Params[i].Name = names[i]
		}
	}
}

// --- the signature registry ---

// Known is one signature this explorer can name, with the parameter names that
// make a decoded call read like the source it was compiled from.
//
// The names are not on the chain and cannot be: a selector is keccak of
// `transfer(address,uint256)`, which contains no names at all. So this is a
// label the explorer supplies, exactly like a block explorer's verified-ABI
// database, only much smaller — and the honest consequence is that a contract
// nobody registered still renders as a selector and raw words.
type Known struct {
	Sig   string
	Names []string
	// Indexed marks which event parameters are topics rather than data. Nil
	// for functions, and nil for an event whose layout is the Solidity
	// default (leading parameters indexed).
	Indexed []bool
}

// KnownSignatures covers the ERC-20 surface plus the demo contracts. It is
// keyed by selector for functions and by topic hash for events, both as
// lowercase hex without the 0x.
var KnownSignatures = func() map[string]Known {
	m := map[string]Known{}
	add := func(k Known, topic bool) {
		key := hex.EncodeToString(Selector(k.Sig))
		if topic {
			key = hex.EncodeToString(Topic(k.Sig))
		}
		m[key] = k
	}
	for _, k := range []Known{
		{Sig: ERC20.TotalSupply},
		{Sig: ERC20.Decimals},
		{Sig: ERC20.BalanceOf, Names: []string{"owner"}},
		{Sig: ERC20.Transfer, Names: []string{"to", "value"}},
		{Sig: ERC20.Approve, Names: []string{"spender", "value"}},
		{Sig: ERC20.Allowance, Names: []string{"owner", "spender"}},
		{Sig: ERC20.TransferFrom, Names: []string{"from", "to", "value"}},
		{Sig: "name()"}, {Sig: "symbol()"},
		{Sig: "increment()"}, {Sig: "get()"}, {Sig: "add(uint256)", Names: []string{"n"}},
	} {
		add(k, false)
	}
	for _, k := range []Known{
		{Sig: ERC20.TransferEvent, Names: []string{"from", "to", "value"},
			Indexed: []bool{true, true, false}},
		{Sig: ERC20.ApprovalEvent, Names: []string{"owner", "spender", "value"},
			Indexed: []bool{true, true, false}},
		{Sig: "Incremented(uint256)", Names: []string{"value"}, Indexed: []bool{false}},
	} {
		add(k, true)
	}
	return m
}()

// DecodeKnownCall decodes calldata against the registry, naming the parameters
// when the selector is one this explorer knows.
func DecodeKnownCall(calldata []byte) Call {
	sig := ""
	var names []string
	if len(calldata) >= 4 {
		if k, ok := KnownSignatures[hex.EncodeToString(calldata[:4])]; ok {
			sig, names = k.Sig, k.Names
		}
	}
	c := DecodeCall(sig, calldata)
	NameParams(&c, names)
	return c
}

// DecodeKnownEvent decodes a log against the registry.
func DecodeKnownEvent(topics [][]byte, data []byte) Call {
	if len(topics) == 0 {
		// An anonymous event, or LOG0. There is no signature hash to look up,
		// so the data is all there is.
		return Call{Note: "no topics — an anonymous event", Params: rawWords(data)}
	}
	k, ok := KnownSignatures[hex.EncodeToString(topics[0])]
	if !ok {
		c := DecodeEvent("", nil, topics, data)
		c.Params = rawWords(data)
		return c
	}
	c := DecodeEvent(k.Sig, k.Indexed, topics, data)
	NameParams(&c, k.Names)
	return c
}

// DecodeReturn decodes a call's return data against the return types of a
// known signature.
//
// Return types are not in the selector either — `balanceOf(address)` hashes
// without its `returns (uint256)` — so they come from the registry too, and a
// contract nobody registered gets its output shown as raw words.
var knownReturns = map[string][]string{
	ERC20.TotalSupply:  {"uint256"},
	ERC20.BalanceOf:    {"uint256"},
	ERC20.Decimals:     {"uint8"},
	ERC20.Allowance:    {"uint256"},
	ERC20.Transfer:     {"bool"},
	ERC20.Approve:      {"bool"},
	ERC20.TransferFrom: {"bool"},
	"name()":           {"string"},
	"symbol()":         {"string"},
	"get()":            {"uint256"},
}

func DecodeReturn(sig string, out []byte) []Param {
	if types, ok := knownReturns[sig]; ok {
		return DecodeParams(types, out)
	}
	if len(out) == 0 {
		return nil
	}
	return rawWords(out)
}

// DecodeUnknownWords renders a byte run as 32-byte words with every reading
// the bytes allow. Exported for callers holding encoded data with no signature
// at all — constructor arguments, most of all, whose types are not recoverable
// from anything on chain.
func DecodeUnknownWords(b []byte) []Param { return rawWords(b) }
