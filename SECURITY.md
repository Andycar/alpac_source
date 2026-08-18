# Security & trust

lampac-go (ALPAC) is a self-hosted server that people run on internet-exposed
VPSes. The predecessor project (Lampac) shut down after exactly this class of
problem — account leaks on exposed instances leading to balancer bans. We take
the exposed-VPS threat model seriously.

## Reporting a vulnerability

**Please report privately — do not open a public issue.**

- Email: `security@alcopa.cc`  *(set this to a real, monitored address)*
- Telegram: a private message to the maintainers via the community group.

Include: affected version, a description, and reproduction steps or a PoC.
We aim to acknowledge within 72 hours.

**Disclosure timeline.** We fix first, ship the fix, then publish an advisory
**at least 14 days after** the fixed release is available, crediting the
reporter unless they prefer to stay anonymous. Please hold public disclosure
until the advisory is out.

### In scope
Anything reachable on a default internet-exposed instance: authentication and
the admin panel, the `/capi` client API, the proxy/stream endpoints, the update
channel, and any path that reaches `exec`/the filesystem from client input.

### Out of scope
Findings that require an already-compromised host or physical access; the legal
status of third-party content sources (that is the operator's responsibility).

## Supply-chain transparency

The server core ships as a compiled binary, so we make its provenance
verifiable without publishing the source:

- **Signed releases.** `SHA256SUMS` is signed with minisign (Ed25519); the
  updater verifies the signature before applying (`require_signature=true`).
  The public key is published out-of-band (Telegram / wiki) so a compromised
  download domain can't swap the binary and the key together.
- **SBOM.** `make sbom` produces a dependency bill of materials (CycloneDX in a
  git checkout, a plain module list otherwise) so the binary's dependencies can
  be checked against CVE feeds. Published with each release.
- **Reproducible-ish builds.** Release binaries are built with `-trimpath` and
  `CGO_ENABLED=0`, so they contain no local filesystem paths and depend on no
  host glibc. Set a fixed `SOURCE_DATE_EPOCH` + `COMMIT` for bit-for-bit builds.

## Source escrow (bus-factor promise)

The engine core is currently closed-source, which is a real bus-factor risk —
the ecosystem watched the original Lampac disappear overnight. Our commitment:

> **If active development of the core stops for good, its source will be
> published under an open license so the community can continue it.**

The client (`ddd-client`), the OpenAPI spec, this Helm chart, the install/
release scripts, and the plugin SDKs are already open.
