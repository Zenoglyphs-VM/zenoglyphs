package evm

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// A transfer must read back as what it is, with the recipient in the spelling
// the sender used.
func TestDecodeTransfer(t *testing.T) {
	const to = "z1qzx2uq2yr3ejwwzh8gxnxtky4m080c9xsmfumy"
	calldata, err := EncodeCall(ERC20.Transfer, to, big.NewInt(2500))
	if err != nil {
		t.Fatal(err)
	}
	c := DecodeKnownCall(calldata)

	if c.Name != "transfer" || c.Signature != ERC20.Transfer {
		t.Fatalf("decoded as %q / %q", c.Name, c.Signature)
	}
	if c.Note != "" {
		t.Fatalf("unexpected note: %s", c.Note)
	}
	if len(c.Params) != 2 {
		t.Fatalf("got %d parameters, want 2", len(c.Params))
	}
	if c.Params[0].Name != "to" || c.Params[0].Type != "address" {
		t.Fatalf("first parameter is %s %s", c.Params[0].Type, c.Params[0].Name)
	}
	if c.Params[0].Zenon != to {
		t.Fatalf("recipient rendered as %q, want %s", c.Params[0].Zenon, to)
	}
	if c.Params[1].Name != "value" || c.Params[1].Value != "2500" {
		t.Fatalf("second parameter is %s = %s", c.Params[1].Name, c.Params[1].Value)
	}
}

// A contract address among the arguments is spelled natively like any other,
// and flagged as something a wallet cannot pay.
func TestDecodedContractAddressIsFlaggedNotHidden(t *testing.T) {
	contract := ContractAddress([]byte("some-asset"))
	calldata, err := EncodeCall(ERC20.BalanceOf, contract)
	if err != nil {
		t.Fatal(err)
	}
	c := DecodeKnownCall(calldata)
	if c.Params[0].Zenon != contract.Zenon() {
		t.Fatalf("a contract argument rendered as %q, want %s", c.Params[0].Zenon, contract.Zenon())
	}
	if c.Params[0].Sendable {
		t.Fatal("a contract argument is marked payable")
	}
	if c.Params[0].Value != contract.Hex() {
		t.Fatalf("value %s, want %s", c.Params[0].Value, contract.Hex())
	}

	// A real recipient is the contrast: same shape, and payable.
	to, _ := ParseAddress("z1qzx2uq2yr3ejwwzh8gxnxtky4m080c9xsmfumy")
	data, _ := EncodeCall(ERC20.Transfer, to, big.NewInt(1))
	if p := DecodeKnownCall(data).Params[0]; !p.Sendable || p.Zenon != to.Zenon() {
		t.Fatalf("an account argument came back %+v", p)
	}
}

// An unknown selector still has to say something useful — how many arguments,
// and what each word could mean — without asserting a type it cannot know.
func TestUnknownSelectorOffersEveryReading(t *testing.T) {
	var buf []byte
	buf = append(buf, 0xde, 0xad, 0xbe, 0xef)
	a, _ := ParseAddress("z1qzx2uq2yr3ejwwzh8gxnxtky4m080c9xsmfumy")
	buf = append(buf, bytes32(new(big.Int).SetBytes(a[:]))...)
	buf = append(buf, bytes32(big.NewInt(42))...)

	c := DecodeKnownCall(buf)
	if c.Selector != "0xdeadbeef" {
		t.Fatalf("selector %s", c.Selector)
	}
	if c.Note == "" {
		t.Fatal("an unknown selector was decoded without a caveat")
	}
	if len(c.Params) != 2 {
		t.Fatalf("got %d words, want 2", len(c.Params))
	}
	// Nothing is claimed to be an address...
	for i, p := range c.Params {
		if p.Type != "word" {
			t.Fatalf("word %d was typed as %q, which is a guess", i, p.Type)
		}
		if p.Hex == "" {
			t.Fatalf("word %d has no raw bytes", i)
		}
	}
	// ...but the address reading is offered where the bytes allow it.
	if c.Params[0].Zenon != "z1qzx2uq2yr3ejwwzh8gxnxtky4m080c9xsmfumy" {
		t.Fatalf("the address reading was not offered: %q", c.Params[0].Zenon)
	}
	// A small integer is address-shaped too, so it also gets the offer — and
	// still reads as the number it is.
	if c.Params[1].Value != "42" {
		t.Fatalf("the integer word read as %q, want 42", c.Params[1].Value)
	}
}

// Events split across topics and data, and decoding all of it from data is the
// classic way to get three wrong answers out of a Transfer.
func TestDecodeEventSplitsTopicsFromData(t *testing.T) {
	from, _ := ParseAddress("z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d")
	to, _ := ParseAddress("z1qzx2uq2yr3ejwwzh8gxnxtky4m080c9xsmfumy")
	topics := [][]byte{
		Topic(ERC20.TransferEvent),
		bytes32(new(big.Int).SetBytes(from[:])),
		bytes32(new(big.Int).SetBytes(to[:])),
	}
	data := bytes32(big.NewInt(2500))

	c := DecodeKnownEvent(topics, data)
	if c.Name != "Transfer" || len(c.Params) != 3 {
		t.Fatalf("decoded as %q with %d parameters", c.Name, len(c.Params))
	}
	if c.Params[0].Value != from.Hex() || c.Params[0].Name != "from" {
		t.Fatalf("from = %s (%s)", c.Params[0].Value, c.Params[0].Name)
	}
	if c.Params[1].Value != to.Hex() || c.Params[1].Name != "to" {
		t.Fatalf("to = %s (%s)", c.Params[1].Value, c.Params[1].Name)
	}
	if c.Params[2].Value != "2500" || c.Params[2].Name != "value" {
		t.Fatalf("value = %s (%s)", c.Params[2].Value, c.Params[2].Name)
	}
}

// Dynamic types have to work, because every real contract returns a string
// somewhere — `name()` and `symbol()` on every token.
func TestDecodeDynamicTypes(t *testing.T) {
	// abi.encode("Glyph") as a single string return: offset, length, padded.
	var out []byte
	out = append(out, bytes32(big.NewInt(32))...)
	out = append(out, bytes32(big.NewInt(5))...)
	word := make([]byte, 32)
	copy(word, "Glyph")
	out = append(out, word...)

	p := DecodeParams([]string{"string"}, out)
	if len(p) != 1 || p[0].Value != "Glyph" {
		t.Fatalf("decoded %q", p[0].Value)
	}

	// And an array of static elements.
	var arr []byte
	arr = append(arr, bytes32(big.NewInt(32))...)
	arr = append(arr, bytes32(big.NewInt(3))...)
	for _, v := range []int64{7, 8, 9} {
		arr = append(arr, bytes32(big.NewInt(v))...)
	}
	q := DecodeParams([]string{"uint256[]"}, arr)
	if len(q) != 1 || len(q[0].Items) != 3 || q[0].Items[2].Value != "9" {
		t.Fatalf("array decoded as %+v", q[0])
	}
}

// The decoder runs against bytes nobody vetted, so nothing in it may panic and
// nothing may allocate on an attacker's say-so.
func TestDecoderSurvivesHostileInput(t *testing.T) {
	huge := strings.Repeat("f", 64) // a length prefix of 2^256-1
	for _, tc := range []struct{ name, calldata string }{
		{"empty", ""},
		{"selector only", "a9059cbb"},
		{"three bytes", "a9059c"},
		{"one short argument", "a9059cbb" + strings.Repeat("00", 31)},
		{"truncated second argument", "a9059cbb" + strings.Repeat("00", 40)},
		{"trailing bytes", "a9059cbb" + strings.Repeat("00", 96)},
		{"absurd string length", "06fdde03" + strings.Repeat("00", 31) + "20" + huge},
		{"offset past the end", "06fdde03" + strings.Repeat("ff", 32)},
		{"array claiming 2^256 items", strings.Repeat("00", 31) + "20" + huge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := hex.DecodeString(tc.calldata)
			if err != nil {
				t.Fatal(err)
			}
			_ = DecodeKnownCall(b)                  // must not panic
			_ = DecodeParams([]string{"string"}, b) // nor here
			_ = DecodeParams([]string{"uint256[]"}, b)
			_ = DecodeKnownEvent([][]byte{}, b)
		})
	}
}

// A selector that matches but arguments that do not is worth saying out loud:
// the values below such a note are not to be trusted.
func TestArgumentLengthMismatchIsReported(t *testing.T) {
	short := append(Selector(ERC20.Transfer), make([]byte, 32)...) // one argument, wants two
	if c := DecodeKnownCall(short); c.Note == "" {
		t.Fatal("a short argument list decoded without a note")
	}
	long := append(Selector(ERC20.Transfer), make([]byte, 96)...) // one too many
	if c := DecodeKnownCall(long); c.Note == "" {
		t.Fatal("trailing bytes decoded without a note")
	}
}

// Return values come back typed, so `0x…01` reads as `true`.
func TestDecodeReturnValues(t *testing.T) {
	if p := DecodeReturn(ERC20.Transfer, bytes32(big.NewInt(1))); len(p) != 1 || p[0].Value != "true" {
		t.Fatalf("a successful transfer returned %+v", p)
	}
	if p := DecodeReturn(ERC20.Decimals, bytes32(big.NewInt(18))); len(p) != 1 || p[0].Value != "18" {
		t.Fatalf("decimals returned %+v", p)
	}
	if p := DecodeReturn("nothing()", nil); p != nil {
		t.Fatalf("an empty return decoded to %+v", p)
	}
}
