#!/usr/bin/env bash
# gen.sh — Regenerate protobuf code for Go and Rust
#
# Usage:
#   ./gen.sh
#
# Requirements:
#   - protoc (Protocol Buffers compiler)
#   - protoc-gen-go   (go install google.golang.org/protobuf/cmd/protoc-gen-go@latest)
#   - protoc-gen-go-grpc (go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest)
#   - Rust/cargo + tonic-build (Rust generation happens automatically via cargo build)
#
# This script regenerates ONLY the Go side.
# The Rust side is regenerated automatically on `cargo build -p wb-core-proto`.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_DIR="$REPO_ROOT/proto"
GO_OUT="$REPO_ROOT/gen/go"
PROTO_FILE="proto/wb/core/v1/core.proto"

echo "==> Generating Go protobuf/gRPC code from $PROTO_FILE"

# Ensure protoc-gen-go and protoc-gen-go-grpc are on PATH.
# They are installed via: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#                          go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
export PATH="$PATH:$(go env GOPATH)/bin"

protoc \
  --proto_path="$PROTO_DIR" \
  --go_out="$GO_OUT" \
  --go_opt=paths=source_relative \
  --go-grpc_out="$GO_OUT" \
  --go-grpc_opt=paths=source_relative \
  "$REPO_ROOT/$PROTO_FILE"

echo "==> Go generation complete:"
echo "    $GO_OUT/wb/core/v1/core.pb.go"
echo "    $GO_OUT/wb/core/v1/core_grpc.pb.go"

echo ""
echo "==> Rust generation happens automatically during: cargo build -p wb-core-proto"
echo "    (tonic-build compiles proto/wb/core/v1/core.proto via build.rs)"
