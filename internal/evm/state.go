package evm

import (
	"encoding/hex"
	"math/big"
	"sort"

	"golang.org/x/crypto/sha3"
)

// Address is a 20-byte EVM account address.
//
// It is deliberately the same width as a NoM address, so an inscriber's own
// account maps across without a derivation step: a NoM address is one type
// byte plus a 19-byte core, and those 20 bytes are the EVM address. The
// mapping is total and injective, which means "who sent this" needs no
// registry and cannot be spoofed — the sender is whoever the chain says
// published the block.
//
// Spellings, parsing and the namespace rules live in address.go.
type Address [20]byte

func AddressFromHex(s string) (Address, error) {
	var a Address
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = s[2:]
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return a, err
	}
	if len(b) != 20 {
		return a, errBadAddress
	}
	copy(a[:], b)
	return a, nil
}

func keccak(parts ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// ContractAddress derives a contract's address from the inscription that
// carries its code.
//
// This is the whole reason the pairing is interesting. Ethereum derives a
// contract address from (sender, nonce), which says nothing about the code and
// requires knowing the sender's history to compute. Here the address is a
// digest of the asset id — a commitment to the *artifact*, because the asset id
// is already a commitment to the bytes (the mint block's hash). So a contract
// address names its own code permanently, and anyone holding the address can
// ask a Zenoglyphs indexer for exactly the bytes that address runs, with no
// trusted registry in between.
//
// The consequence worth stating plainly: deployed code here is immutable in a
// stronger sense than on Ethereum. There is no CREATE2-plus-SELFDESTRUCT
// redeploy trick, because the address is a function of the code, not of who
// deployed it or when.
//
// # The type byte, and why it is not part of the hash
//
// The first byte is forced to ProtocolAddrByte (0x16) and only the remaining 19
// come from the digest. That is a deliberate loss of eight bits of a 160-bit
// space, bought for two properties that matter more:
//
//   - **A contract address can never collide with a real account.** Every NoM
//     keypair lands on byte 0 = 0x00, so an unconstrained keccak[12:] would put
//     roughly one contract in every 256 inside genuine user space — an address
//     someone could hold the key to, and one that renders as an ordinary
//     `z1q…` a wallet will happily pay.
//   - **It can never land in NoM's embedded-contract space** (byte 0 = 0x01),
//     where a send is handed to the contract sequencer and refused.
//
// So every contract address is structurally unreachable by any keypair, the
// same property A-26 established for the burn sink, and it says so on its face:
// contracts read `z1z…`, accounts read `z1q…`. What is left is 152 bits, which
// is 2^76 work to find any colliding pair — not 2^80, and still far past
// anything a deployer grinding inscription content can reach.
//
// This changes every contract address, so it is a consensus change for this
// metaprotocol and not a refactor. It is why the devnet deployment was redone
// from scratch rather than migrated; see research/evm-address-scheme.md.
func ContractAddress(assetID []byte) Address {
	var a Address
	a[0] = ProtocolAddrByte
	copy(a[1:], keccak(assetID)[13:])
	return a
}

// Account is one EVM account. Code is nil for an externally-owned account.
type Account struct {
	Nonce   uint64
	Balance *big.Int
	Code    []byte
	Storage map[string]*big.Int // 32-byte key hex -> value
}

func newAccount() *Account {
	return &Account{Balance: new(big.Int), Storage: map[string]*big.Int{}}
}

// State is the derived world state. It is not stored on chain and is not
// consensus in the Zenoglyphs sense: it is a projection, recomputable from the
// inscription log in canonical order, exactly like every other projection in
// this codebase (A-18's "the event log is the source of truth").
//
// # Reads that write
//
// The interpreter's reads go through get, which creates the account it is
// asked about. That is part of the derivation — an account an execution only
// *read* is still in Accounts() and still in Root() — so it is kept exactly,
// and every replay reaches the same root it always did. But it has to stay
// inside the interpreter (the unexported code/balance/nonce/load below). The
// exported getters are pure: an explorer looking up an address must not add
// it to the state it is showing, and readers sharing one State under a read
// lock must not write to its maps.
//
// # The journal
//
// Every write records how to undo itself, so snapshot/revert is a mark and a
// rewind instead of a deep copy of the world. The copy made each call O(state)
// and so a replay O(txs × state): the rebuild measured 55 µs per transaction
// at 2 k transactions and 80 µs at 4 k (research/evm-benchmark.md). Values are
// only ever replaced, never mutated in place, so an undo is restoring the old
// pointer. The journal is dropped by Settle once no snapshot can be
// outstanding — between top-level transactions.
type State struct {
	accounts map[Address]*Account
	journal  []func()
}

func NewState() *State { return &State{accounts: map[Address]*Account{}} }

// get is the interpreter's account access: it creates what it reads.
func (s *State) get(a Address) *Account {
	acc, ok := s.accounts[a]
	if !ok {
		acc = newAccount()
		s.accounts[a] = acc
		s.journal = append(s.journal, func() { delete(s.accounts, a) })
	}
	return acc
}

// emptyAccount is what the pure getters report for an address with no
// account. It is never written.
var emptyAccount = newAccount()

// peek is get without the side effect.
func (s *State) peek(a Address) *Account {
	if acc, ok := s.accounts[a]; ok {
		return acc
	}
	return emptyAccount
}

func (s *State) Exists(a Address) bool { _, ok := s.accounts[a]; return ok }

// Len is the number of accounts, without sorting them the way Accounts does.
func (s *State) Len() int { return len(s.accounts) }

// Pure reads, for anything outside the interpreter.
func (s *State) Code(a Address) []byte                   { return s.peek(a).Code }
func (s *State) Nonce(a Address) uint64                  { return s.peek(a).Nonce }
func (s *State) Balance(a Address) *big.Int              { return new(big.Int).Set(s.peek(a).Balance) }
func (s *State) StorageOf(a Address) map[string]*big.Int { return s.peek(a).Storage }
func (s *State) Load(a Address, key *big.Int) *big.Int {
	return loadFrom(s.peek(a), key)
}

// Interpreter reads, which create the account (see "Reads that write").
func (s *State) code(a Address) []byte      { return s.get(a).Code }
func (s *State) nonce(a Address) uint64     { return s.get(a).Nonce }
func (s *State) balance(a Address) *big.Int { return new(big.Int).Set(s.get(a).Balance) }
func (s *State) load(a Address, key *big.Int) *big.Int {
	return loadFrom(s.get(a), key)
}

func loadFrom(acc *Account, key *big.Int) *big.Int {
	if v, ok := acc.Storage[hex.EncodeToString(bytes32(key))]; ok {
		return new(big.Int).Set(v)
	}
	return new(big.Int)
}

func (s *State) SetCode(a Address, c []byte) {
	acc := s.get(a)
	old := acc.Code
	acc.Code = c
	s.journal = append(s.journal, func() { acc.Code = old })
}

func (s *State) SetNonce(a Address, n uint64) {
	acc := s.get(a)
	old := acc.Nonce
	acc.Nonce = n
	s.journal = append(s.journal, func() { acc.Nonce = old })
}

func (s *State) setBalance(acc *Account, v *big.Int) {
	old := acc.Balance
	acc.Balance = v
	s.journal = append(s.journal, func() { acc.Balance = old })
}

func (s *State) AddBalance(a Address, v *big.Int) {
	acc := s.get(a)
	s.setBalance(acc, norm(new(big.Int).Add(acc.Balance, v)))
}

func (s *State) SubBalance(a Address, v *big.Int) bool {
	acc := s.get(a)
	if acc.Balance.Cmp(v) < 0 {
		return false
	}
	s.setBalance(acc, new(big.Int).Sub(acc.Balance, v))
	return true
}

func (s *State) Store(a Address, key, val *big.Int) {
	acc, k := s.get(a), hex.EncodeToString(bytes32(key))
	old, had := acc.Storage[k]
	if val.Sign() == 0 {
		delete(acc.Storage, k) // a zero slot is indistinguishable from an unset one
	} else {
		acc.Storage[k] = new(big.Int).Set(val)
	}
	s.journal = append(s.journal, func() {
		if had {
			acc.Storage[k] = old
		} else {
			delete(acc.Storage, k)
		}
	})
}

// Accounts returns every account address, sorted, so callers that render or
// hash state get a deterministic order without each inventing one.
func (s *State) Accounts() []Address {
	out := make([]Address, 0, len(s.accounts))
	for a := range s.accounts {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i][:]) < string(out[j][:]) })
	return out
}

// Root is a digest over the whole world state.
//
// It exists for the same reason Store.ProjectionDigest does: two independent
// derivers that have read the same inscriptions must be able to prove in one
// comparison that they agree, rather than diffing account by account. It is
// not a Merkle-Patricia root and deliberately not compatible with Ethereum's —
// nothing here needs to prove a single account to a light client, and a
// sorted-concatenation digest is auditable by eye.
func (s *State) Root() string {
	h := sha3.NewLegacyKeccak256()
	for _, a := range s.Accounts() {
		acc := s.accounts[a]
		h.Write(a[:])
		h.Write(bytes32(new(big.Int).SetUint64(acc.Nonce)))
		h.Write(bytes32(acc.Balance))
		h.Write(keccak(acc.Code))
		keys := make([]string, 0, len(acc.Storage))
		for k := range acc.Storage {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			kb, _ := hex.DecodeString(k)
			h.Write(kb)
			h.Write(bytes32(acc.Storage[k]))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// snapshot/revert give CALL and REVERT their all-or-nothing semantics: a
// snapshot is a position in the journal, and reverting undoes everything
// written since, newest first. Reverting to a mark leaves the state exactly
// as a deep copy taken at that mark would have — which is what this replaced,
// and what TestEVMReplayOfDevnetHistory holds it to.
func (s *State) snapshot() int { return len(s.journal) }

func (s *State) revert(mark int) {
	for i := len(s.journal) - 1; i >= mark; i-- {
		s.journal[i]()
	}
	s.journal = s.journal[:mark]
}

// Settle forgets how to undo what has been written so far. Call it only when
// no snapshot is outstanding — Chain.Apply does, after each top-level
// transaction — or a later revert would rewind past the snapshot's mark into
// entries that no longer exist.
func (s *State) Settle() { s.journal = s.journal[:0] }

// Copy is an independent deep copy, for readers that need to run code against
// the state without writing to the one they share (Chain.View).
func (s *State) Copy() *State {
	c := NewState()
	for a, acc := range s.accounts {
		n := &Account{Nonce: acc.Nonce, Balance: new(big.Int).Set(acc.Balance),
			Code: acc.Code, Storage: make(map[string]*big.Int, len(acc.Storage))}
		for k, v := range acc.Storage {
			n.Storage[k] = new(big.Int).Set(v)
		}
		c.accounts[a] = n
	}
	return c
}
