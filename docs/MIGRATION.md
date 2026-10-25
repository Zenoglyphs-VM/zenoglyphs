# Legacy scope and project direction

This document explains the boundary between the deprecated Zenoglyphs repository
and the separate ZVM repositories. It supplies no converter, performs no state
transfer and establishes no accepted migration procedure or live compatibility.
Support commitments remain [unestablished](../SUPPORT.md#version-and-maintenance-status).

## Reviewed source entry points

| Source | What the reviewed material describes |
| --- | --- |
| [Legacy specification](../spec/SPEC.md) at `716d0b16672f43dd644b189d7af21c6767260540` | The retained inscriptions metaprotocol and its wire/state rules |
| [ZVM architecture](https://github.com/Zenoglyphs-VM/zvm-docs/blob/35c5be8a59059a9e1f3aab40c980ef6cd18cf0be/ARCHITECTURE.md) | The separate repositories and their based EVM relationships |
| [ZVM Core](https://github.com/Zenoglyphs-VM/zvm-core/blob/29e6d9b30be94de3772c850c7a1e9811585a8833/README.md) | Shared derivation, chain/genesis, encoding and browser components |
| [ZVM Node](https://github.com/Zenoglyphs-VM/zvm-node/blob/ae90b945b0ea95887c31cfb2f19e0b88504186d8/README.md) | Momentum derivation into EVM blocks and an execution-client/API/indexing process |

The [source record](LEGACY_SOURCE.json) retains exact reviewed revisions and
selected digests. These are source entry points for evaluating project direction,
not an accepted release or automatic replacement for an existing installation.
The reviewed entry points do not supply a qualified transfer procedure from
legacy inscription state to the separate ZVM chain state. This is a limit of
the reviewed materials, not a search of all operator data or repository history.

## Before considering an actual transition

1. Privately identify the existing source, chain/configuration, database schema,
   assets and exact operation hashes/outcomes. Preserve the original installation,
   evidence and independently recoverable backups; keep secrets out of inventories
   intended for publication.
2. Obtain an authorized target/version decision and an explicit compatibility or
   conversion procedure. Independently review its state, ownership, accounting,
   replay, finality, epoch and recovery assumptions. Do not point a new executable
   at a legacy database or infer equivalence from matching names.
3. Rehearse that accepted procedure on owned copies with synthetic inputs. Record
   source and input/output digests, actual commands/results and incomplete cases.
   Check restoration and exact accounting without replacing uncertain original
   transactions or reconstructing missing evidence from summaries.
4. Only the separately authorized maintainer/operator process can accept a real
   cutover, distribution, signing or chain action. Retain the old source, records,
   licenses and rollback limits. Documentation or green build/test steps cannot
   supply that decision.

The original README's build/devnet examples are preserved as historical material.
They are not a migration rehearsal or a current deployment qualification.
This documentation change ran no application, wallet, signing, chain request,
deployment, database conversion or live asset transfer.
