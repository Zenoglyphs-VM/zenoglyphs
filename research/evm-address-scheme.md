# Native addresses, and what it costs to have them

**Status:** **implemented**, and the devnet deployment was re-launched under it
on 2026-09-10. Companion to [`evm-fee-design.md`](evm-fee-design.md) and
[`evm-todo.md`](evm-todo.md).

Every figure below is measured on the devnet at chain 69, or read out of
go-zenon, not recalled.

---

## 1. The question

Can someone use their Syrius extension and their ordinary `z1…` address with
this EVM, or do they need a second identity?

**They already could, and nobody could see it.** A NoM address is 20 bytes; an
EVM address is 20 bytes; `AddressOf` was already the identity function on those
bytes. So the key in the extension was always the key to the matching EVM
account — no registration, no derivation, no second keypair.

What was missing was every part of the system that *shows* or *accepts* an
address. The identity was native and the interface was not.

## 2. What changed

| | before | after |
|---|---|---|
| sender identity | already the NoM address | unchanged — this was never the gap |
| rendering | `0x…` everywhere | `z1…` everywhere, with `0x…` kept as the key |
| parsing | hex only | either spelling, at every boundary |
| bech32 decoding | checksum **stripped, not checked** | verified |
| contract addresses | `keccak(assetId)[12:]` | `0x16 ‖ keccak(assetId)[13:]` |
| publishing | CLI only | CLI, or the browser via Syrius |

### 2.1 The checksum was not being checked

`decodeBech32Address` sliced the last six characters off and trusted the rest.
That is safe for an issuer address read off the chain, which is where it was
used, and it is not safe for an address a human pastes into a transfer. Every
single-character substitution is now rejected; `TestTyposAreRejected` proves it
over all 31 substitutions at all 32 data positions.

### 2.2 Contract addresses are outside every reachable namespace

The first byte of a NoM address is a namespace, and which values are reachable
is structural rather than conventional:

```
0x00  user      types.PubKeyToAddress hardcodes it, so every keypair lands here
0x01  embedded  NoM's own contracts; a send here goes to the contract sequencer
0x16  ours      no keypair and no NoM contract can reach it
```

An unconstrained `keccak[12:]` puts roughly **one contract in every 256 inside
genuine user space** — an address someone could hold a key to, and one that
renders as an ordinary `z1q…` that a wallet will happily pay. Forcing byte 0 to
`0x16` costs eight bits of a 160-bit space and buys:

- a contract address can never collide with an account;
- it can never land in embedded-contract space, where a send is refused;
- it says what it is on sight — accounts read `z1q…`, contracts read `z1z…`.

152 bits remain, which is 2⁷⁶ work to find any colliding pair. That is far past
what a deployer grinding inscription content can reach.

**This changes every contract address, which is why it is a re-launch and not a
refactor.** See §5.

### 2.3 Every address is rendered natively, and "payable" is a separate flag

The first attempt rendered contracts as hex, on the reasoning that a bech32
contract address is a string a wallet will pay and nothing can refund. It was
wrong twice over, and measuring it is what showed that.

**It hid almost everything.** On the devnet listing, 15 of 17 addresses had no
native spelling — 8 contracts and 7 addresses left behind by the re-launch. The
spelling exists for the audience that reads `z1…`, and it was being withheld
from them two thirds of the time.

**It was inconsistent with this protocol's own convention.** Zenoglyphs already
publishes `z1zen0glyphxdurn…` for the burn sink and `z1zen0glyphxescr0w…` for
dispenser escrow. Both are exactly as unspendable as a contract, both sit in
the same `0x16` namespace, and both are rendered natively on purpose — because
the namespace *is* the warning.

So the fact is carried explicitly instead:

```json
{"address": "0x1668…", "zenon": "z1ze5z8s…", "sendable": false, "namespace": "protocol"}
{"address": "0x0035…", "zenon": "z1qq6eg8…", "sendable": true,  "namespace": "user"}
```

A flag is a stronger signal than an omission: a UI can render it, and the
contract page and the ABI decoder both do — "Not a payable address — no keypair
can reach the protocol namespace". An absence can only be noticed by someone who
already knew to look.

The prefix separates the two on sight (`z1q…` a wallet, `z1z…` a contract), but
`sendable` is the authority: one bech32 character spans eight type bytes, so
`z1q…` covers `0x00`–`0x07` while only `0x00` is a real account.

## 3. Can existing ERC-20 contracts be ported?

**Yes, unmodified, if they are compiled for shanghai or below.** This was
measured rather than argued: `internal/evm/testdata/erc20-solc.sol` is a
conventional ERC-20 — mappings of mappings, indexed events, `require` with
messages, a constructor that mints — compiled by solc 0.8.26 with the optimizer
on, and it is deployed and exercised on devnet.

```
deploy   374,152 gas   of a 1,450,208 plasma-derived budget
transfer  13,244 gas
```

The address change is **not** a porting concern, and this is the part worth
stating plainly: bech32 is a display encoding. Solidity sees 20 raw bytes and
the ABI puts them in a right-aligned 32-byte word, exactly as on Ethereum. No
contract observes the spelling, so no contract needs changing.

### What does not port

Asserted against the interpreter directly, not inferred from a failing call:

| missing | what it blocks |
|---|---|
| `CREATE`, `CREATE2` | contract factories |
| `DELEGATECALL`, `CALLCODE` | proxies, upgradeable contracts |
| `MCOPY`, `TLOAD`, `TSTORE` | cancun-era codegen and transient storage |
| `EXTCODECOPY`, `EXTCODEHASH` | code introspection |
| `SELFDESTRUCT` | — |
| `BLOCKHASH`, `COINBASE`, `PREVRANDAO`, `GASLIMIT`, `BASEFEE`, `GASPRICE` | block-context reads |

A self-contained token, vault or registry needs none of these. A proxy pattern
or a factory needs one and cannot be deployed at all.

**The porting instruction is one flag:** `solc --evm-version shanghai`. The
cancun build of the test contract happens to run, and that is luck rather than
support — a contract doing more memory copying would emit `MCOPY` and fail.

## 4. Publishing from the browser

An EVM call is an ordinary inscription: a send carrying the Zenoglyphs envelope
in its data field. So the extension needs to know nothing about EVMs — it is a
signer, and it will sign any account block a page hands it. The page decides
`toAddress`, `amount`, `tokenStandard` and `data`; Syrius supplies the account,
the public key, the chain position, the plasma and the signature.

That means the browser must build a **byte-identical** envelope, because the
asset id is sha3 over those bytes and the indexer rejects anything its own
encoder could not have produced. `web/ui/src/wallet/` is therefore bound to the
Go side by `npm run verify:vectors`, which checks:

- the protocol's own golden vectors (`testdata/vectors/v1.json`);
- **a real inscription taken off the chain, byte for byte** — the transfer at
  devnet momentum 53641, which the Go inscriber produced and a node accepted;
- the ERC-20 selectors against published constants (keccak-256 with the pre-NIST
  `0x01` padding, which is the easiest thing here to get subtly wrong);
- the address codec both directions, and that a one-character typo is refused.

CBOR, keccak and bech32 are implemented locally rather than pulled from npm, for
the reason the Go side implements them locally: the bytes have to be exactly
right, the algorithms are frozen, and a dependency is one `npm update` away from
encoding a small integer in two bytes for somebody else's good reason.

The plasma is deliberately left at zero for the extension to fill. Under dynamic
plasma the price moves per momentum, so a page computing its own plasma would be
computing it against a price that may have changed by the time the user
approves.

### 4.1 The faucet was sending tokens nowhere

Worth recording as its own finding, because it is the sharpest illustration of
why "both are 20 bytes" is a trap as well as an opportunity.

The faucet read its recipient from **MetaMask**, via `window.ethereum`. An
Ethereum address is 20 bytes, so it parsed, the ABI encoded it, the transfer
succeeded, and a balance appeared in the explorer. It was unspendable on
arrival: an Ethereum keypair produces an address whose first byte is whatever
the hash gave, and only `0x00` is a NoM user account, so **no Zenon key can
ever sign an inscription from it.** The tokens were visible, correct, and
permanently stuck.

Two changes, because either alone leaves the hole:

- the page connects **Syrius**, whose account is by construction one that can
  publish calls afterwards (the EIP-1193 path is removed, not disabled — if it
  ever returns it should return with a warning attached);
- the server **refuses any recipient that is not a user account**, so pasting
  an Ethereum address by hand fails with a sentence saying why rather than
  succeeding into a void.

The general lesson is the one `Sendable` exists for: identical width does not
mean interchangeable, and the check has to be on the type byte rather than on
the parse succeeding.

## 5. What the re-launch cost, measured

Re-deriving the devnet under the new scheme, from momentum 1:

```
63 EVM transactions
 8 deploys           — all succeeded, at new addresses
 1 call that ran
54 calls that did nothing
```

**The 54 did not fail. They succeeded and had no effect**, because their `to`
names a pre-relaunch address that now holds no code, and a call to a codeless
address returns empty successfully — exactly Ethereum's behaviour.

That is the sharpest argument in this document for why an address change is a
re-launch. A rule change that made old calls *fail* would be visible. This one
makes them succeed while doing nothing, which is the failure mode that does not
announce itself. It is also why `Chain.FeeFrom` is an activation height rather
than a flag day: every inscription published before a rule must stay valid on
replay forever, and history cannot be re-signed.

## 6. Open

| | |
|---|---|
| **Deploy from the browser** | The write panel calls contracts; it does not deploy them. Init code routinely exceeds the 16 KB single-block ceiling and needs the multi-part chaining `zcribe` does. |
| **Dynamic types in the write panel** | Static 32-byte types only. A function taking `string` or `bytes` cannot be called from the page, only from the CLI. |
| **A signature registry** | Functions are named from a hard-coded table. A contract nobody registered shows raw selectors and raw words — correct, and much less useful. Verified-ABI upload is the obvious next step. |
| **Re-launch debris** | Seven addresses in the devnet listing are neither accounts nor contracts: they are the pre-relaunch contract addresses the 54 no-op calls named. They render natively and are correctly flagged unreachable, but they are noise, and a fresh chain would not have them. |
| **`unknownSelectors`** | The test ERC-20 reports 14 unnamed dispatcher selectors against 9 named ones. Most are solc-internal, but the number is high enough that the interface list should not be read as complete. |
