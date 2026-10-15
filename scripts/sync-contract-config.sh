#!/usr/bin/env bash
set -euo pipefail

# Keep the caller's working directory: ./qrypt.toml is resolved there.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
go run "$SCRIPT_DIR/sync-contract-config" "$@"
