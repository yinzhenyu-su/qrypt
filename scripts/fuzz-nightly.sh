#!/usr/bin/env bash
# Nightly fuzzing: each property fuzzer runs for a fixed budget. Failures
# write their corpus entry under <pkg>/testdata/fuzz/<FuzzName>/, which is
# committed so the regression runs in every normal `go test` from then on.
#
# Run via CI nightly workflow or manually:
#   ./scripts/fuzz-nightly.sh          # every fuzzer, 30s each
#   ./scripts/fuzz-nightly.sh 10s      # custom per-fuzzer budget
set -euo pipefail
cd "$(dirname "$0")/.."

budget="${1:-30s}"
FUZZERS=(
  "pkg/vfs:FuzzCleanVirtualPath"
  "pkg/crypt:FuzzCipherSegmentRoundtrip"
  "pkg/crypt:FuzzRcloneConfigObscureRoundtrip"
  "pkg/config:FuzzParseSizeAndDuration"
  "pkg/drive:FuzzErrorCategoryMessage"
  "pkg/syncer:FuzzParseCompareMode"
  "pkg/media:FuzzPassthroughVirtualFileBounds"
)

failed=0
for fuzzer in "${FUZZERS[@]}"; do
  IFS=: read -r pkg fn <<< "$fuzzer"
  echo "== fuzz $pkg/$fn ($budget) =="
  if ! go test -run='^$' -fuzz="$fn" -fuzztime="$budget" "./$pkg"; then
    echo "!! fuzzer $pkg/$fn found a failure; corpus saved under $pkg/testdata/fuzz/$fn"
    echo "   CI: download the 'fuzz-corpus' artifact and commit the new"
    echo "       corpus entries (see docs/for-developer/fuzz-corpus.md):"
    echo "         gh run download <run-id> -n fuzz-corpus -D /tmp/fuzz-corpus"
    echo "         cp /tmp/fuzz-corpus/$pkg/testdata/fuzz/$fn/* $pkg/testdata/fuzz/$fn/"
    echo "         git add $pkg/testdata && git commit"
    failed=1
  fi
done

if [ "$failed" -ne 0 ]; then
  echo "== fuzz nightly FAILED; commit the new corpus entries =="
  exit 1
fi
echo "== fuzz nightly passed =="
