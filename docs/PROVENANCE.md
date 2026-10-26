# Legacy source and distribution provenance

## Retained original source

The reviewed original default-branch source is
`716d0b16672f43dd644b189d7af21c6767260540`, with Git tree
`31e161ffbd1899b2d64686e76c2a4f7c5f97bd23`. That selected source has one reachable
history commit and 323 retained top-level blob files. This describes the reviewed
Git snapshot; it does not reconstruct development history that is absent from it.

The [source record](LEGACY_SOURCE.json) pins that identity and selected original
specification, guide, assumptions, review and license/notice bytes. The original
README remains intact below its new legacy notice. The original security-policy
text is retained before the added maintenance/distribution section. Historical
reviews and specifications keep their original content and dates; they are not
fresh execution, a current audit or proof that all findings remain present or
have been fixed. Do not copy private diagnostics out of retained historical text.

The existing dependency-review candidate is
`adc3c7017ee9b13956803018bd4f269ebddd1562`. Its three changed paths are the Go CI
workflow, dependency-update configuration and [review guide](DEPENDENCY_REVIEW.md).
The legacy documentation extension preserves those inputs and every other
previously tracked source outside its explicit README/security/support/provenance
scope. Original commits remain ancestors; no history rewrite or archival is
part of this change.

## Build and distribution boundary

The original `go-zenon` gitlink is
`590f43ca7a24dccd8e883bc8903a0cb02e54528a`, with its source URL retained in
[`.gitmodules`](../.gitmodules). The effective local replacement in
[`go.mod`](../go.mod) must be reviewed with that actual submodule source.
No submodule or dependency was fetched for this documentation qualification.

The current [Go workflow](../.github/workflows/ci.yml) builds with committed
`web/dist` content. It does not rebuild or qualify the UI source/lock against
that embedded output. A successful build/vet/test job does not bind a release
binary, regenerated viewer, supported platform or signed distribution to the
reviewed source. The [dependency and distribution review](DEPENDENCY_REVIEW.md)
retains those unresolved input/output, SBOM, provenance and independent review
requirements.

[`LICENSE`](../LICENSE) and [`NOTICE.md`](../NOTICE.md) retain the project's
declared GPL-3.0 material and third-party notices unchanged. Preserve original
attribution and corresponding source records when reviewing a particular
distribution. This source inventory does not complete an output-specific license
or dependency/reachability review or grant a new distribution permission.

Public metadata on 2026-10-25 showed no GitHub releases or tags. A committed asset,
source snapshot or CI status is not evidence of a currently supported, signed
release. Any new release, support commitment or repository archival remains a
separate authorized maintainer decision. Keep provenance summaries in English
without personal identities, local paths, credentials, wallet material or raw
private diagnostics.
