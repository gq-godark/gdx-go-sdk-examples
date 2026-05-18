// gdx-go-sdk-examples -- feat/v2 (MM distribution)
//
// Self-contained darkpool trading examples. The `godark` SDK is vendored
// under `sdk/` (Go source with pre-generated protobuf bindings already
// committed under `sdk/proto/`), so building this repository requires only
// the standard Go toolchain plus the public modules referenced below --
// no extra `protoc` tooling, no extra system dependencies.
module github.com/gq-godark/gdx-go-sdk-examples

go 1.22

require github.com/gq-godark/gdx-go-sdk v0.1.0

require (
	github.com/coder/websocket v1.8.13 // indirect
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/crypto v0.32.0 // indirect
	google.golang.org/protobuf v1.36.4 // indirect
)

replace github.com/gq-godark/gdx-go-sdk => ./sdk
