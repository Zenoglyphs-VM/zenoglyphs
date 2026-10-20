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
