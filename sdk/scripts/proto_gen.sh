#!/usr/bin/env bash
# Regenerate Go protobuf bindings from `gdx-proto/proto`.
#
# By default this reads the submodule at `gdx-proto/`. Override with
# `GDX_PROTO_ROOT` (path to the `proto/` directory) to point at a different
# checkout - this is what the Layer 1 listener does when bumping the pin.
#
# Output: `proto/gdx/{common,edge,health,sequencer}/v1/*.pb.go`. We only commit the
# four proto packages the SDK actually depends on, matching python / rust /
# js (which don't ship orchestrator / replication / settlement / mpc / etc.).
#
# Requires:
#   - protoc (>= 3.21)
#   - protoc-gen-go v1.36.4 EXACTLY (newer versions switched from concatenated
#     string raw descriptors to byte-slice form, which breaks byte-for-byte
#     reproducibility on CI). Install with:
#       go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.4
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
cd "$REPO_ROOT"

DEFAULT_PROTO_ROOT="$(cd "$REPO_ROOT/gdx-proto/proto" 2>/dev/null && pwd || echo "")"
PROTO_ROOT="${GDX_PROTO_ROOT:-$DEFAULT_PROTO_ROOT}"

if [[ -z "$PROTO_ROOT" || ! -d "$PROTO_ROOT" ]]; then
  echo "error: proto source not found at: ${PROTO_ROOT:-<unset>}" >&2
  echo "       run 'git submodule update --init' or set GDX_PROTO_ROOT." >&2
  exit 1
fi

if ! command -v protoc >/dev/null 2>&1; then
  echo "error: 'protoc' not found in PATH" >&2
  exit 1
fi

if ! command -v protoc-gen-go >/dev/null 2>&1; then
  if [[ -x "${GOPATH:-$HOME/go}/bin/protoc-gen-go" ]]; then
    export PATH="${GOPATH:-$HOME/go}/bin:$PATH"
  else
    echo "error: 'protoc-gen-go' not found. Install with:" >&2
    echo "  go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.4" >&2
    exit 1
  fi
fi

# Enforce the pinned protoc-gen-go version so local regens stay byte-for-byte
# stable against CI (which installs v1.36.4 explicitly). Mismatched versions
# produce different raw-descriptor encodings and trip the proto_drift check.
PINNED_PROTOC_GEN_GO="v1.36.4"
ACTUAL_PROTOC_GEN_GO="$(protoc-gen-go --version 2>/dev/null | awk '{print $NF}')"
if [[ "$ACTUAL_PROTOC_GEN_GO" != "$PINNED_PROTOC_GEN_GO" ]]; then
  echo "error: protoc-gen-go ${ACTUAL_PROTOC_GEN_GO} found; this repo pins ${PINNED_PROTOC_GEN_GO}." >&2
  echo "       go install google.golang.org/protobuf/cmd/protoc-gen-go@${PINNED_PROTOC_GEN_GO}" >&2
  exit 1
fi

OUT_DIR="$REPO_ROOT/proto"
rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

echo "Regenerating Go protobuf bindings from $PROTO_ROOT ..."

# The `Mgdx/<...>.proto=...` rewrites point each proto's `option go_package`
# at this module's import path so the generator emits the right import paths
# for cross-file references between common <-> edge <-> health <-> sequencer.
protoc \
  --proto_path="$PROTO_ROOT" \
  --go_out="$OUT_DIR" \
  --go_opt=module=github.com/gq-godark/gdx-go-sdk/proto \
  --go_opt=Mgdx/common/v1/types.proto=github.com/gq-godark/gdx-go-sdk/proto/gdx/common/v1 \
  --go_opt=Mgdx/edge/v1/edge.proto=github.com/gq-godark/gdx-go-sdk/proto/gdx/edge/v1 \
  --go_opt=Mgdx/health/v1/health.proto=github.com/gq-godark/gdx-go-sdk/proto/gdx/health/v1 \
  --go_opt=Mgdx/sequencer/v1/sequencer.proto=github.com/gq-godark/gdx-go-sdk/proto/gdx/sequencer/v1 \
  "$PROTO_ROOT"/gdx/common/v1/types.proto \
  "$PROTO_ROOT"/gdx/edge/v1/edge.proto \
  "$PROTO_ROOT"/gdx/health/v1/health.proto \
  "$PROTO_ROOT"/gdx/sequencer/v1/sequencer.proto

echo "Done. Bindings written to:"
find "$OUT_DIR" -name '*.pb.go' | sort
