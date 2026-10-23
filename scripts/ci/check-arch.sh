#!/usr/bin/env bash
# Checks the real Go import graph plus the upload-domain ownership seam.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

go run ./scripts/ci/checkarch
