#!/usr/bin/env bash
# Layered test entry points. Each layer is a separate command so CI and
# developers can run exactly the coverage they need:
#
#   ./scripts/test-layers.sh fast         # default unit suite
#   ./scripts/test-layers.sh changed      # only what the working diff can reach
#   ./scripts/test-layers.sh race         # concurrency-heavy packages under -race
#   ./scripts/test-layers.sh vfs-stability # VFS upload engine flake guard (x3)
#   ./scripts/test-layers.sh smoke        # localfs mount smoke test
#   ./scripts/test-layers.sh all          # everything except real-netdisk tests
#   ./scripts/test-layers.sh integration  # real provider/HTTP contract tests
#
# Every layer prints its wall-clock duration and warns past $BUDGET seconds.
# `fast` runs the whole module and costs ~22s on an 8-core box, so 25s is a
# regression tripwire rather than a target: pkg/vfs alone is ~16s of it (281
# tests, none parallel, most of them waiting on timers), the rest of the module
# plus compilation is ~10s. Splitting pkg/vfs out would not fix that -- it is
# the default suite's coverage, not a stray slow package -- so a quick local
# run is the `changed` layer's job, not a smaller `fast`.
#
# Real-netdisk contract tests are NOT part of go test: they need a running
# `qrypt mount` debug server with mounted cloud accounts. Run them manually:
#   qrypt debug test contract --socket /tmp/qrypt.sock --mount <name>
#
# Windows CI (ci.yaml test-windows, windows-latest) runs the main-module
# package list from the race layer instead of `fast`, because `go test
# ./...` also compiles the vendored cgofuse tests whose Windows host
# expects a real WinFsp mount.
set -euo pipefail
cd "$(dirname "$0")/.."

# Wall-clock regression tripwire for a single measure() step, in seconds. See
# the header for why `fast` sits just under it.
BUDGET=25

# Reclaim temp left behind by a test binary that was killed or hit -timeout:
# such a process never reaches its own exit path, so the isolated CLI home, the
# shared session-log roots, and the debug-server sockets under the process temp
# root outlive it. Running this at the start of every layer makes each
# invocation heal the previous one. Only qrypt-named entries are swept -- `Test*`
# directories are t.TempDir's generic naming and belong to every Go tree on the
# host, not just this one -- and a failed run's own temp is deliberately left
# for inspection.
clean_orphan_temp() {
  # Resolve the temp root the way os.TempDir() does, so the sweep looks where
  # the tests wrote under them: $TMPDIR on Unix, %TMP% then %TEMP% on Windows
  # (Git Bash hands those over backslashed).
  local tmp="${TMPDIR:-${TMP:-${TEMP:-/tmp}}}"
  tmp="${tmp//\\//}"
  [ -d "$tmp" ] || return 0
  # Best-effort: a leftover another process still holds open is common on
  # Windows and must not abort the layer that follows.
  rm -rf "$tmp"/qrypt-cli-test-home-* "$tmp"/qrypt-core-test-logs \
    "$tmp"/qrypt-mobile-test-logs "$tmp"/qrypt-mount-test.* || true
  # Test sockets carry a nanosecond timestamp
  # (pkg/control/server_test.go, internal/cli/debug/debug_test.go). Keeping the
  # numeric prefix means a live server at a hand-picked path such as
  # /tmp/qrypt.sock is never removed.
  local sock
  for sock in "$tmp"/qrypt-[0-9]*.sock "$tmp"/qrypt-test-[0-9]*.sock; do
    [ -e "$sock" ] || continue
    rm -f "$sock" || true
  done
}
clean_orphan_temp

measure() {
  local label="$1"; shift
  local start
  start=$(date +%s)
  if [ "$1" = "go" ] && [ "$2" = "test" ] && [ "${3:-}" = "-json" ]; then
    # JSON mode: stream to a temp file so the slowest packages can be
    # reported afterwards. The grep -q exit code drives the result.
    local tmp json_ok
    tmp=$(mktemp)
    "$@" | tee "$tmp" >/dev/null || true
    json_ok=0
    grep -q '"Action":"fail"' "$tmp" && json_ok=1
    if [ "$json_ok" -ne 0 ]; then
      printf '== FAILURES in %s ==\n' "$label"
      awk -F'"' '
        /"Action":"fail"/ && /"Test":"[^"]+"/ {
          for (i=1;i<=NF;i++) if ($i=="Test") print "  FAIL " $(i+2)
        }
      ' "$tmp" | head -20
      # The names alone do not say whether a deadline, an assertion, or a
      # teardown failed, and replaying the whole stream just buries it under
      # whatever else was talking (fuzz seeding is loud), so replay only the
      # output belonging to the tests that failed.
      printf '== failure detail ==\n'
      awk -F'"' '
        /"Action":"fail"/ && /"Test":"[^"]+"/ {
          for (i=1;i<=NF;i++) if ($i=="Test") print $(i+2)
        }
      ' "$tmp" | sort -u | while IFS= read -r name; do
        printf '  --- %s\n' "$name"
        awk -F'"' -v want="$name" '
          /"Action":"output"/ && index($0, "\"Test\":\"" want "\"") {
            for (i=1;i<=NF;i++) {
              if ($i=="Output") {
                out=$(i+2); gsub(/\\t/, "  ", out); sub(/\\n$/, "", out)
                if (out != "") print "    " out
              }
            }
          }
        ' "$tmp" | tail -25
      done
    fi
    printf '== slowest packages ==\n'
    awk -F'"' '
      /"Action":"pass"/ && !/"Test":"/ && /"Package":"[^"]+"/ && /"Elapsed":[0-9.]+/ {
        pkg=$0; sub(/.*"Package":"/, "", pkg); sub(/".*/, "", pkg)
        el=$0; sub(/.*"Elapsed":/, "", el); sub(/[^0-9.].*/, "", el)
        print el, pkg
      }
    ' "$tmp" | sort -rn | head -10 | while read -r t p; do
      printf '  %6.2fs  %s\n' "$t" "$p"
    done
    # CI: write the per-package timings into the step summary so a >20s
    # regression can be traced without re-running locally. Separate the
    # layers by job name via the caller-provided label.
    if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
      {
        echo "### $label"
        awk -F'"' '
          /"Action":"pass"/ && !/"Test":"/ && /"Package":"[^"]+"/ && /"Elapsed":[0-9.]+/ {
            pkg=$0; sub(/.*"Package":"/, "", pkg); sub(/".*/, "", pkg)
            el=$0; sub(/.*"Elapsed":/, "", el); sub(/[^0-9.].*/, "", el)
            print "| " pkg " | " el "s |"
          }
        ' "$tmp" | sort -t'|' -k3 -rn | head -10
        echo
      } >> "$GITHUB_STEP_SUMMARY"
    fi
    rm -f "$tmp"
    local end=$(( $(date +%s) - start ))
    printf '== %s: %ds ==\n' "$label" "$end"
    if [ "$end" -ge "$BUDGET" ]; then
      printf '== WARNING: %s took %ds (budget %ds); see the slowest-package list above ==\n' "$label" "$end" "$BUDGET"
    fi
    exit "$json_ok"
  fi
  "$@"
  local end=$(( $(date +%s) - start ))
  printf '== %s: %ds ==\n' "$label" "$end"
  if [ "$end" -ge "$BUDGET" ]; then
    printf '== WARNING: %s took %ds (budget %ds); see the slowest-package list above ==\n' "$label" "$end" "$BUDGET"
  fi
}

# run_repeated <repeats> <command...> runs the command REPEATS times as
# independent processes and fails when any run fails. Every test in the
# repeated suites uses its own t.TempDir(), so the runs share no state; the
# suites are timer-bound rather than CPU-bound, so overlapping them costs a
# fraction of the wall clock that `-count=N` spends running the same code
# paths back to back.
run_repeated() {
  local repeats="$1"; shift
  local tmp i status=0
  tmp=$(mktemp -d)
  local pids=() logs=()
  for ((i = 0; i < repeats; i++)); do
    logs+=("$tmp/run-$i.log")
    "$@" >"${logs[$i]}" 2>&1 &
    pids+=("$!")
  done
  for i in "${!pids[@]}"; do
    if ! wait "${pids[$i]}"; then
      status=1
      printf '== FAIL: repetition %d/%d ==\n' "$((i + 1))" "$repeats"
      cat "${logs[$i]}"
    fi
  done
  rm -rf "$tmp"
  return "$status"
}

# ── Delta selection ─────────────────────────────────────────────────
# `changed` runs only the packages the working diff can reach. A package's test
# binary is built from its own files plus every package it imports, test
# imports included, so a change landing outside that closure cannot change its
# result. `go list -deps -test` is the authority for the closure; a plain
# `.Deps` listing is not enough, because pkg/vfs's tests import pkg/crypt and
# pkg/drivers/localfs, which its non-test deps never mention.
#
# Every step fails safe: an unplaceable diff widens the run to the whole module
# rather than risking a silent skip. That covers no git checkout, no HEAD yet,
# an empty change set, a changed file under no package directory (scripts/,
# go.mod, the //go:build tools stub at the root), or a failed go list. The
# selection is printed so the narrowing stays auditable, and `fast` keeps
# running everything for CI and pre-push.

# diff_packages prints the import path of every package the working diff
# touches -- staged, unstaged and untracked -- and flags files it cannot place
# with a leading "!". A file is attributed to the innermost package directory
# containing it, walking up so testdata and fixtures land on their package.
# Multi-line payloads go through files rather than awk -v, which rejects them.
diff_packages() {
  local root work rc
  root=$(git rev-parse --show-toplevel 2>/dev/null) || return 1
  work=$(mktemp -d) || return 1

  {
    git diff --name-only HEAD -- 2>/dev/null
    git ls-files --others --exclude-standard 2>/dev/null
  } | sort -u > "$work/files"
  go list -f '{{.Dir}}{{"\t"}}{{.ImportPath}}' ./... 2>/dev/null > "$work/dirs"

  if [ ! -s "$work/files" ] || [ ! -s "$work/dirs" ]; then
    rm -rf "$work"
    return 1
  fi

  awk -v root="$root" '
    NR == FNR {
      if ($0 == "") next
      split($0, field, "\t")
      pkgdir[field[1]] = field[2]
      next
    }
    {
      path = root "/" $0
      while (path != "" && path != "/") {
        if (path in pkgdir) { print pkgdir[path]; next }
        if (path == root) break
        if (!sub(/\/[^\/]*$/, "", path)) break
      }
      print "!" $0
    }
  ' "$work/dirs" "$work/files"
  rc=$?

  rm -rf "$work"
  return "$rc"
}

# affected_packages prints the packages whose test closure covers one of the
# changed packages, and returns 1 when the diff cannot be placed.
affected_packages() {
  local seeds module work rc
  seeds=$(diff_packages) || return 1
  [ -n "$seeds" ] || return 1
  case "$seeds" in *'!'*) return 1 ;; esac

  module=$(go list -m 2>/dev/null) || return 1
  work=$(mktemp -d) || return 1

  printf '%s\n' "$seeds" > "$work/seeds"
  go list -deps -test -f '{{.ImportPath}}{{"\t"}}{{join .Deps " "}}' ./... \
    2>/dev/null > "$work/graph"

  if [ ! -s "$work/graph" ]; then
    rm -rf "$work"
    return 1
  fi

  awk -F'\t' -v module="$module" '
    NR == FNR { changed[$1] = 1; next }
    {
      if ($1 == "") next
      dep[$1] = $2
      node[$1] = 1
    }
    END {
      # Own packages of this module, minus the synthetic build roots below.
      prefix = module "/"
      for (p in node) {
        if (p ~ /\.test/ || p ~ / \[/) continue
        if (p == module || index(p, prefix) == 1) mine[p] = 1
      }

      for (p in mine) {
        delete seen
        top = 0
        stack[++top] = p
        # Tests live in the ".test" build roots, so the walk starts there too --
        # that is the only place test-only imports attach.
        if ((p ".test") in node)              stack[++top] = p ".test"
        if ((p " [" p ".test]") in node)      stack[++top] = p " [" p ".test]"
        if ((p "_test [" p ".test]") in node) stack[++top] = p "_test [" p ".test]"

        hit = 0
        while (top > 0) {
          n = stack[top--]
          if (n in seen) continue
          seen[n] = 1
          if (n in changed) { hit = 1; break }
          nd = split(dep[n], d, " ")
          for (i = 1; i <= nd; i++)
            if (d[i] != "" && !(d[i] in seen)) stack[++top] = d[i]
        }
        if (hit) print p
      }
    }
  ' "$work/seeds" "$work/graph"
  rc=$?

  rm -rf "$work"
  return "$rc"
}

case "${1:-fast}" in
  fast)
    # -json so the per-package wall clock is reported; the slowest 10
    # packages pinpoint where the fast layer's budget goes.
    measure "fast: go test -json ./..." go test -json -count=1 ./...
    ;;
  changed)
    # What the working diff can actually reach; see Delta selection above for
    # the safety rules. Falls back to the whole module when the diff cannot be
    # placed on packages.
    pkgs=$(affected_packages) || pkgs=""
    if [ -z "$pkgs" ]; then
      echo "== changed: diff cannot be placed on packages; running the whole module =="
      measure "changed: go test -json ./... (full fallback)" go test -json -count=1 ./...
    else
      count=$(printf '%s\n' "$pkgs" | wc -l | tr -d ' ')
      printf '== changed: %s package(s) reachable from the working diff ==\n' "$count"
      printf '%s\n' "$pkgs" | sed 's/^/     /'
      # shellcheck disable=SC2086 -- the list is deliberately split into argv.
      measure "changed: go test -json ($count pkg)" go test -json -count=1 $pkgs
    fi
    ;;
  race)
    measure "race: pkg/vfs drive drivers contracttest drivecopy control logging mount syncer cli core mobile crypt task cmd" \
      go test -race -count=1 ./pkg/vfs ./pkg/drive ./pkg/drivers/... \
      ./pkg/contracttest ./pkg/vfs/drivecopy ./pkg/control ./pkg/logging ./pkg/mount ./pkg/syncer ./internal/cli \
      ./pkg/core ./pkg/mobile ./pkg/crypt ./pkg/task ./cmd/qrypt
    ;;
  vfs-stability)
    # The async upload engine (worker shutdown, staging cleanup, journaling)
    # is timing sensitive; three independent runs catch the occasional flake.
    measure "vfs-stability: 3x go test -count=1 ./pkg/vfs" \
      run_repeated 3 go test -count=1 ./pkg/vfs/
    ;;
  smoke)
    measure "smoke: localfs" scripts/smoke-localfs.sh
    ;;
  all)
    measure "all: default suite" go test -count=1 ./...
    measure "all: race vfs+core" go test -race -count=1 ./pkg/vfs ./pkg/core
    measure "all: localfs smoke" scripts/smoke-localfs.sh
    ;;
  integration)
    # Requires a running debug server with real cloud mounts; see the
    # contract-test comment above. Each mount runs the full CRUD/contract
    # suite, so pass only the mounts you actually have configured.
    : "${QRYPT_CONTRACT_SOCKET:?integration layer needs QRYPT_CONTRACT_SOCKET (e.g. /tmp/qrypt.sock)}"
    mounts=("$@")
    if [ "${#mounts[@]}" -le 1 ]; then
      echo "usage: $0 integration <mount> [mount...]" >&2
      exit 2
    fi
    for m in "${mounts[@]:1}"; do
      measure "integration: contract $m" \
        go run ./cmd/qrypt debug test contract --socket "$QRYPT_CONTRACT_SOCKET" --mount "$m"
    done
    ;;
  *)
    echo "usage: $0 [fast|changed|race|vfs-stability|smoke|all|integration <mount>...]" >&2
    exit 2
    ;;
esac
