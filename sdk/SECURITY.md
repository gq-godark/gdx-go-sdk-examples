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

- Handshake: `Noise_XK_25519_AESGCM_SHA256` (initiator). The sequencer
  static public key is pinned via `ClientConfig.NoiseStaticPublicKeyHex`
  or `GDX_NOISE_STATIC_PUBLIC_KEY` / `GDX_NOISE_STATIC_PUBKEY`.
- Prologue: `gdx-noise-xk/v1\0` concatenated with the 16-byte user UUID.
- Transport AEAD: AES-256-GCM with empty Noise associated data. Application
  headers (`OrderHeader` / `ResponseHeader`) are bound as
  `SHA256(header) || plaintext` inside the ciphertext.
- Cleartext routing fields: monotonic send nonce, body length, and
  `conn_id` remain in the JSON/protobuf header for edge routing; they are
  not the Noise AEAD nonce.
