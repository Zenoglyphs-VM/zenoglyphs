# contracts

Source for every artifact deployed to devnet. There is no `solc` and no wasm
toolchain in this environment, so all of it is hand-assembled — these scripts
*are* the source, and without them the bytecode is unreproducible.

Re-running them reproduces all four artifacts byte-identical to what is on
chain, which is the property `research/evm-prototype.md` §3.1 rests on: a
contract address is `0x16 ‖ keccak(assetId)[13:]`, so an address is a
commitment to the artifact, and an artifact nobody can rebuild is a commitment
to nothing.

```sh
cd evm  && python3 build_counter.py 20d8a6f5a693f9d1d627a598e8820f7a55ee74c183aa8f1a30e8d4e8dd9a8d84
        && python3 erc20.py
cd wasm && python3 build_poc.py && python3 build_engine.py
```

## evm/

| file | what |
|---|---|
| `asm.py` | a small EVM assembler: labels, PUSH sizing, init-code wrapping |
| `build_counter.py` | `increment()` / `get()` / `add(uint256)`, 133 B runtime |
| `erc20.py` | a full ERC-20, 686 B runtime — storage slot 0 supply, mapping 1 balances, mapping 2 allowances |

The counter's build takes the `Incremented(uint256)` topic as an argument
because keccak lives on the Go side; the value is in the command above.

## wasm/

| file | what |
|---|---|
| `asm.py` | a wasm assembler: sections, LEB128, locals, globals, control flow |
| `build_poc.py` | `lib.wasm` (155 B Mandelbrot kernel) and `app.wasm` (248 B renderer that imports it) |
| `build_engine.py` | `engine.wasm` (745 B) — the stateful version: owns viewport, pan, zoom and framebuffer |
| `index.html`, `live.html` | the pages that fetch these through composite slots and link them at instantiation |

`live.html` is the interactive one. It holds no state and does no arithmetic
beyond mapping client coordinates to canvas pixels; everything else is in the
module.
