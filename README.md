# Zenoglyphs

A metaprotocol for Zenon's Network of Momentum: inscriptions carried in
ordinary 0-amount account blocks, indexed by anyone, rendered by anything. No
consensus change, no new embedded contract, no protocol fee: plasma already
prices data.

`zcribe` is the reference indexer, API, content origin, viewer and CLI in one
binary.

- [`spec/SPEC.md`](spec/SPEC.md): the frozen v1 wire format
- [`ASSUMPTIONS.md`](ASSUMPTIONS.md): every judgement call, and what it costs
  to change
- [`deploy/`](deploy/): running it on a server, with the Telegram bot

## Build

Needs Go 1.23+. go-zenon is a submodule pinned to the upstream commit this was
built against, so clone with `--recurse-submodules`; hashing, PoW and keys come
from the consensus implementation rather than a copy.

```sh
go build -o zcribe ./cmd/zcribe
go test ./...
```

`go build` embeds the viewer from `web/dist/`, which is committed. Rebuild it
after changing `web/ui/`:

```sh
cd web/ui && npm install --allow-git=all && npm run build
```

## Run against the devnet

```sh
cd go-zenon && make devnet-up
./zcribe serve -start 1 -confirmations 3   # http://localhost:8710/
./zcribe demo -devnet                      # one inscription of every kind
```

`-devnet` signs with go-zenon's committed devnet phrase: public by design,
worthless outside chain 69.

GPL-3.0, as go-zenon is; see [NOTICE.md](NOTICE.md). The full guide, with how
it works and why: [docs/GUIDE.md](docs/GUIDE.md).
