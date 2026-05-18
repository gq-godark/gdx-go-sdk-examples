# Security policy

## Supported versions

Security fixes are applied on the default branch for this SDK. Once the SDK
publishes tagged versions, patch releases may be used for backports; until
then, build from source against the latest commit on `main`.

## Reporting a vulnerability

Please report security issues through your organization's standard channel
to the GoDark engineering team, or open a **private** security advisory on
the repository hosting this SDK if that option is available.

**Do not** file public GitHub issues for undisclosed vulnerabilities.

## Dependency review

CI runs `govulncheck` against declared dependencies on every push and PR.
Run locally with:

```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

## TLS

For production, connect to `wss://` endpoints. The default base URL
(`wss://api.godark-dex.com`) speaks TLS. To pin a custom CA bundle or
present a client certificate, build a `*websocket.DialOptions` via the
underlying transport (see `internal/transport`) -- the public client API
surface for TLS overrides will be added in a follow-up PR alongside REST.

## Cryptographic construction

This SDK is wire-compatible with the python / rust / js / cpp / java
reference implementations:

- ECDH: X25519 over `crypto/ecdh`.
- KDF: HKDF-SHA256 with info `b"gdx-e2e-session-key-v1"` and salt
  `min(local_pub, remote_pub) || max(local_pub, remote_pub)` (byte-lex
  ordering of the raw 32-byte public keys).
- AEAD: AES-256-GCM, 96-bit nonce of
  `session_id(u64 BE) || nonce_counter(u32 BE)`, protobuf header bytes as
  AAD, ciphertext-with-tag appended in the `encrypted` field.
- Replay protection: per-session monotonic-counter `NonceTracker` rejects
  duplicates and out-of-order rewinds beyond a 1024-entry window.
