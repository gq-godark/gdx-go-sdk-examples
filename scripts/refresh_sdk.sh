#!/usr/bin/env bash
# Refresh the vendored SDK source under `sdk/` from a sibling `gdx-go-sdk`
# checkout AND record the upstream commit in `sdk/UPSTREAM_REF` so the
# release pipeline (scripts/package.sh + .github/workflows/release.yml) can
# verify the vendored copy hasn't drifted from upstream.
#
# Pre-generated protobuf bindings under `proto/gdx/**/*.pb.go` are included
# so the distribution does not require `protoc` or `protoc-gen-go`.
#
# Usage:
#   ./scripts/refresh_sdk.sh /path/to/gdx-go-sdk
#
# The source checkout MUST:
#   1. be a git checkout (`.git/` present) so the pin can be recorded
#   2. have a clean worktree (no uncommitted changes); otherwise the recorded
#      SHA wouldn't faithfully describe what was vendored
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 /path/to/gdx-go-sdk" >&2
  exit 1
fi

SRC="$1"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DEST="$REPO_ROOT/sdk"

if [[ ! -d "$SRC" ]]; then
  echo "error: source directory '$SRC' does not exist" >&2
  exit 1
fi
if [[ ! -d "$SRC/.git" ]]; then
  echo "error: '$SRC' is not a git checkout - pin cannot be recorded" >&2
  exit 1
fi
if [[ ! -f "$SRC/go.mod" ]]; then
  echo "error: '$SRC/go.mod' missing - this is not a gdx-go-sdk checkout" >&2
  exit 1
fi

# Refuse to refresh from a dirty upstream worktree. The pin would not be
# reproducible and the CI parity check would fail in confusing ways.
if ! git -C "$SRC" diff --quiet || ! git -C "$SRC" diff --cached --quiet; then
  echo "error: upstream '$SRC' has uncommitted changes; commit or stash first" >&2
  exit 1
fi

UPSTREAM_SHA="$(git -C "$SRC" rev-parse HEAD)"
UPSTREAM_TAG="$(git -C "$SRC" describe --tags --exact-match HEAD 2>/dev/null || true)"

echo "Refreshing $DEST from $SRC ..."
echo "  upstream HEAD: $UPSTREAM_SHA${UPSTREAM_TAG:+ (tag $UPSTREAM_TAG)}"

rm -rf "$DEST"
mkdir -p "$DEST"

# Copy SDK source. The goal is to vendor *only* what is needed to import
# `github.com/gq-godark/gdx-go-sdk` and compile against it from inside the
# bundle -- the Go-idiomatic equivalent of a .whl / .a artifact. Concretely:
#
# Drop repo-level cruft (not part of the importable package):
#   - .git/, .github/                  VCS + CI
#   - scripts/                         SDK maintainer tooling
#   - gdx-proto/                       proto submodule (we ship pre-generated
#                                      bindings under proto/, so consumers
#                                      don't need protoc)
#   - .gitmodules, .gitignore          VCS artefacts
#   - README.md, CHANGELOG.md,         repo docs -- recipients use the
#     SECURITY.md                        bundle/ docs at the zip root
#   - .env.example                     upstream's repo-level template; the
#                                      bundle ships its own .env.example
#                                      at the zip root that's tuned for the
#                                      bundled examples
#   - examples/                        upstream's in-repo examples are a
#                                      maintainer-facing convenience; the
#                                      bundle ships its own examples/ at
#                                      the zip root and `replace`s godark
#                                      to ./sdk
#   - *_test.go, testdata/             huge size win; recipients don't run
#                                      upstream tests
rsync -a \
  --exclude='.git/' \
  --exclude='.github/' \
  --exclude='scripts/' \
  --exclude='gdx-proto/' \
  --exclude='.gitmodules' \
  --exclude='.gitignore' \
  --exclude='README.md' \
  --exclude='CHANGELOG.md' \
  --exclude='SECURITY.md' \
  --exclude='.env' \
  --exclude='.env.example' \
  --exclude='.env.*' \
  --exclude='/examples/' \
  --exclude='*_test.go' \
  --exclude='testdata/' \
  "$SRC/" "$DEST/"

# Pin the commit (prefer tag for human readability if HEAD is on one).
if [[ -n "$UPSTREAM_TAG" ]]; then
  echo "$UPSTREAM_TAG" > "$DEST/UPSTREAM_REF"
else
  echo "$UPSTREAM_SHA" > "$DEST/UPSTREAM_REF"
fi
echo "  wrote pin: $(cat "$DEST/UPSTREAM_REF")  -> sdk/UPSTREAM_REF"

echo "Vendored size: $(du -sh "$DEST" | cut -f1)"
echo "Done. Review with: cd '$REPO_ROOT' && git status sdk/"
