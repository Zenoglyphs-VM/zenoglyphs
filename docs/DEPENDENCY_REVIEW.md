# Dependency and distribution review

CI actions use full commit pins with readable major-version comments. Weekly
Dependabot proposals cover Actions, the root Go module and the UI npm lock,
limited to two open ordinary version-update pull requests per ecosystem. Review
and qualification are required before accepting a proposal. The configuration
does not establish that the updater has run or that an advisory or distribution
review is complete.

## Resolve the actual source and build inputs

For an action update, resolve the release in its official repository and review
the old-to-new implementation, metadata, bundled dependencies, permissions,
credentials, network destinations and artifact handling. Keep the full commit
and readable version comment together. Matching a tag alone does not review the
implementation.

Review `go.mod` and `go.sum` with the actual Go toolchain and platform.
`github.com/zenon-network/go-zenon` is replaced by `./go-zenon`, whose source is
the exact Git submodule commit at the URL in [`.gitmodules`](../.gitmodules).
Record and review that gitlink separately from module proposals. Hashing, PoW
and key behavior depend on the effective replacement source. Build, vet,
relevant behavior tests and integrity checks must use the accepted submodule
commit; the placeholder module version is not its source identity.

`web/ui/go.mod` is an intentional module boundary that excludes UI files and
`node_modules` from the parent Go traversal. It is not a separately built Go
application and is not a second Go updater root. Review real UI inputs through
`web/ui/package.json` and `web/ui/package-lock.json`. Preserve exact resolved
versions, integrity and install-script decisions. The `nom-ui` dependency comes
from Git: bind the lock's complete commit ID and review its source and transitive
inputs rather than treating the manifest's GitHub shorthand as a pinned release.

The current Go workflow builds with committed `web/dist` bytes. It does not
rebuild or typecheck `web/ui`, nor run its lint/vector commands. A dependency
update must separately qualify those steps in a bounded environment and compare
the resulting distribution with the reviewed UI source and build inputs. Green
Go CI alone does not establish that the embedded viewer matches the updated
source or lock.

Triage advisories against exact resolved versions or replacement commits and the
advisory revision. Identify the call site or build step, runtime/build/test role
and affected shipped output. Preserve unknown reachability and failed/skipped
checks. Use the repository's private security channel for sensitive findings
until coordinated disclosure; scanner presence or absence is not a complete
reachability decision.

## Review a particular distribution

Start with [`NOTICE.md`](../NOTICE.md) and [`LICENSE`](../LICENSE). Review the
project/go-zenon GPL material, the separately attributed p5.js test asset and all
UI assets, fonts, linked dependencies and transitive material that reaches the
actual output. Retain original license/notice text, exact source revisions,
resolved input and output digests, build settings and corresponding source
material. Independent distribution/license review remains required; a notice
table or package license field alone is not a completed review.

Bind an SBOM and provenance statement to the exact binary and embedded viewer,
rather than only a manifest dependency list. The `ubuntu-latest` label and
downloaded Go toolchain remain mutable inputs. Record actual runner/toolchain
identities, supported-platform results, output-bound notices and release
signatures separately. These checks do not authorize signing, live inscription
publication, deployment or a production release.

Keep public artifacts in English and omit private paths, personal identity,
credentials, wallet material and raw private diagnostics. Preserve exact source
and run identities, actual exits, completeness and explicit unknowns in
sanitized evidence summaries.

References: [GitHub Actions secure use](https://docs.github.com/en/actions/reference/security/secure-use)
and [Dependabot options](https://docs.github.com/en/code-security/reference/supply-chain-security/dependabot-options-reference).
