# Security policy

Report a vulnerability privately, through this repository's **Security** tab,
**Report a vulnerability**. Please don't open a public issue or pull request
for it.

Say what you found, how to reproduce it, and what it lets someone do. The
reply comes in the same private advisory.

## Scope

zcribe indexes Zenon mainnet as well as the devnet, so this matters most:
anything that makes two honest indexers disagree, runs content outside the
sandboxed content origin, or reaches the admin panel without authorisation.

The `-devnet` phrase is go-zenon's committed devnet mnemonic, public by design
and worthless outside chain 69; it is not a finding.

## Legacy maintenance and distribution

The repository is marked deprecated; see [support status](SUPPORT.md) and
[source/distribution provenance](docs/PROVENANCE.md). The private reporting
guidance and original mainnet/devnet scope above remain applicable. Deprecation
does not establish a maintenance service, supported-version window, response
deadline, bounty, qualified release or promise to repair every historical report.

Confirm that the affected repository's private reporting route is available
before submitting confidential material. The policy text itself does not enable
that feature. If no verified private route is available, keep details private
while establishing an authorized contact; public contact requests must omit
finding details. GitHub's [private reporting instructions](https://docs.github.com/en/code-security/how-tos/report-and-fix-vulnerabilities/report-privately)
describe the platform route. Review attachments and advisory credit choices,
and omit personal identity, local paths, credentials, signing material and raw
operator data. Any disclosure, patch sharing, additional recipient or release
requires its own applicable authorization.
