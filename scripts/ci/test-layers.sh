#!/usr/bin/env bash
# Layered test entry points. Each layer is a separate command so CI and
# developers can run exactly the coverage they need:
#
#   ./scripts/ci/test-layers.sh fast         # default unit suite
#   ./scripts/ci/test-layers.sh changed      # only what the working diff can reach
#   ./scripts/ci/test-layers.sh race         # concurrency-heavy packages under -race
#   ./scripts/ci/test-layers.sh race-changed # only the -race packages the diff can reach
#   ./scripts/ci/test-layers.sh race-shard 1/4        # one balanced shard of the race set
#   ./scripts/ci/test-layers.sh race-shard-all [4]    # every shard, in parallel, locally
#   ./scripts/ci/test-layers.sh vfs-stability # VFS tree flake guard (x3, read included)
#   ./scripts/ci/test-layers.sh smoke        # localfs mount smoke test
#   ./scripts/ci/test-layers.sh all          # everything except real-netdisk tests
#   ./scripts/ci/test-layers.sh integration  # real provider/HTTP contract tests
#
# Every layer prints its wall-clock duration and warns past $BUDGET seconds.
# Set QRYPT_TEST_TIMINGS=1 to also print the slowest 10 individual tests on the
# layers that run `go test -json` (fast/changed/race-changed/race-shard).
# The timings below were measured on an 8-core darwin/arm64 dev box with Go
# 1.27.4 and a warm build cache; CI runners are slower and vary, so read them
# as order-of-magnitude only.
#
# `fast` runs the whole module and costs ~26s, so 25s is a regression tripwire
# rather than a target. The pkg/vfs tree dominates it: pkg/vfs ~9s,
# pkg/vfs/read ~14s and pkg/vfs/upload ~8s. Even with t.Parallel these tests
# mostly wait on timers, so splitting the tree out would not fix that -- it is
# the default suite's coverage, not a stray slow package -- so a quick local
# run is the `changed` layer's job, not a smaller `fast`.
#
# The depth layers sit above the fast budget by design. `race` now covers the
# whole pkg/vfs subtree and takes ~39s with the default race_parallel on the
# same box (pkg/vfs/read alone costs 26.5s of the 218s summed package time);
# `vfs-stability` is three concurrent runs of the pkg/vfs subtree and takes
# ~22s.
# Their WARNING lines are informational, not a regression signal.
#
# PR CI runs `race-changed` instead of `race`: it reuses `changed`'s
# import-closure selection (and its fail-safe to the whole race set when the
# diff cannot be placed on packages) but executes those packages under -race:
# a one-file driver change reaches 11 packages and took 14s on the same box,
# while a pkg/vfs/read change reaches 28 of the 47 packages. main pushes and the
# nightly job keep the full `race` layer as the backstop. Nightly also shards
# the race set into
# `race-shard i/n` jobs, one balanced shard per runner: the four heavy packages
# (pkg/vfs/read, internal/cli, pkg/crypt, pkg/drive) are pinned one per shard,
# the rest is packed cheapest-fit by measured cost, so the slowest shard costs
# about one heavy package instead of the whole set.
#
# Real-netdisk contract tests are NOT part of go test: they need a running
# `qrypt mount` debug server with mounted cloud accounts. Run them manually:
#   qrypt probe test contract --config ./qrypt.toml --mount <name>
#
# Windows CI (ci.yaml test-windows, windows-latest) runs the main-module
# package list from the race layer instead of `fast`, because `go test
# ./...` also compiles the vendored cgofuse tests whose Windows host
# expects a real WinFsp mount.
set -euo pipefail
# Resolve the physical path: delta selection compares `go list` absolute package
# dirs against the git toplevel, and on systems where the checkout is reached
# through a symlink (macOS /tmp -> /private/tmp) a logical path never matches.
cd "$(cd "$(dirname "$0")/../.." && pwd -P)"

# Wall-clock regression tripwire for a single measure() step, in seconds. See
# the header for why `fast` sits just under it, and why the race/vfs-stability
# depth layers exceeding it is expected.
BUDGET=25

# The race layer runs many independent package test binaries. The default
# package parallelism is GOMAXPROCS, but these suites are mostly timer/IO
# bound, so a few extra in-flight packages hide their waits. This changes
# scheduling only: every test and every package still runs under -race.
# Override with RACE_P when a runner needs a different memory/CPU balance.
race_parallel() {
  local cpus p
  cpus=$(getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)
  p=$((cpus * 4))
  [ "$p" -gt 32 ] && p=32
  [ "$p" -lt 8 ] && p=8
  printf '%s' "${RACE_P:-$p}"
}

# The race layer's package set, shared by `race`, `race-changed`'s fallback and
# the sharding path. Wildcards stay in pattern form for the layers so packages
# added later are picked up automatically; only `race-shard` needs the concrete
# list, and it expands these patterns through `go list` itself.
RACE_PATTERNS=(
  ./pkg/vfs/...
  ./pkg/drive
  ./pkg/drivers/...
  ./pkg/contracttest
  ./pkg/control
  ./pkg/logging
  ./pkg/mount
  ./pkg/syncer
  ./internal/cli
  ./pkg/core
  ./pkg/mobile
  ./pkg/crypt
  ./pkg/task
  ./cmd/qrypt
)

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
  # Test sockets carry a nanosecond timestamp. Runtime sockets for the
  # mount-debug helper live under its temporary QRYPT_HOME and are removed
  # with that work directory.
  local sock
  for sock in "$tmp"/qrypt-[0-9]*.sock "$tmp"/qrypt-test-[0-9]*.sock; do
    [ -e "$sock" ] || continue
    rm -f "$sock" || true
  done
}
clean_orphan_temp

# slowest_tests <json file> [limit] prints "<elapsed>\t<package>\t<test>" for
# the most expensive individual test passes in a `go test -json` stream, most
# expensive first. Package-level Elapsed hides which test owns a regression;
# QRYPT_TEST_TIMINGS=1 surfaces the per-test view. Ties sort by package then
# test name so the report is stable across runs.
slowest_tests() {
  awk -v limit="${2:-10}" '
    /"Action":"pass"/ && /"Test":"[^"]+"/ && /"Elapsed":[0-9.]+/ {
      n++
      el=$0; sub(/.*"Elapsed":/, "", el); sub(/[^0-9.].*/, "", el)
      pkg=$0; sub(/.*"Package":"/, "", pkg); sub(/".*/, "", pkg)
      tst=$0; sub(/.*"Test":"/, "", tst); sub(/".*/, "", tst)
      eval[n] = el + 0
      epkg[n] = pkg
      etst[n] = tst
    }
    END {
      for (i = 1; i <= n; i++) order[i] = i
      for (i = 2; i <= n; i++) {
        v = order[i]
        for (j = i - 1; j >= 1; j--) {
          if (eval[order[j]] > eval[v]) break
          if (eval[order[j]] == eval[v] && epkg[order[j]] "." etst[order[j]] < epkg[v] "." etst[v]) break
          order[j + 1] = order[j]
        }
        order[j + 1] = v
      }
      for (i = 1; i <= n && i <= limit; i++) {
        k = order[i]
        printf "%.2f\t%s\t%s\n", eval[k], epkg[k], etst[k]
      }
    }
  ' "$1"
}

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
    # Per-test timings are opt-in: the package list already spots the package
    # that grew, and the test-level view is wide enough that it only earns its
    # place when someone is narrowing down a regression.
    if [ "${QRYPT_TEST_TIMINGS:-0}" = "1" ]; then
      printf '== slowest tests ==\n'
      slowest_tests "$tmp" 10 | while IFS=$'\t' read -r t p name; do
        printf '  %6.2fs  %s  %s\n' "$t" "$p" "$name"
      done
    fi
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
        if [ "${QRYPT_TEST_TIMINGS:-0}" = "1" ]; then
          echo
          echo "#### Slowest tests"
          echo
          echo "| test | package | elapsed |"
          echo "| --- | --- | --- |"
          slowest_tests "$tmp" 10 | while IFS=$'\t' read -r t p name; do
            printf '| %s | %s | %ss |\n' "$name" "$p" "$t"
          done
        fi
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
# `changed` runs only the packages the selected diff can reach. A package's test
# binary is built from its own files plus every package it imports, test
# imports included, so a change landing outside that closure cannot change its
# result. `go list -deps -test` is the authority for the closure; a plain
# `.Deps` listing is not enough, because pkg/vfs's tests import pkg/crypt and
# pkg/drivers/localfs, which its non-test deps never mention.
#
# QRYPT_DIFF_BASE may point at a commit for push-time validation; otherwise the
# working-tree diff against HEAD is used. Every step fails safe: an unplaceable diff widens the run to the whole module
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
    git diff --name-only "${QRYPT_DIFF_BASE:-HEAD}" -- 2>/dev/null
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

# ── Race sharding ───────────────────────────────────────────────────
# `race` runs one go test over the whole set, so its wall clock is set by the
# slowest packages: pkg/vfs/read, internal/cli, pkg/crypt and pkg/drive are
# most of the run. `race-shard i/n` cuts the concrete package list into n
# shards that CI runs as independent jobs, so the longest shard costs about one
# heavy package instead of the whole set. The costs below are the per-package
# Elapsed sums measured with
#   go test -race -count=1 -p 32 <RACE_PATTERNS>
# on the 8-core darwin/arm64 dev box the header describes (47 packages, sum
# 218s, 39s wall). They only steer the split, so a stale number shifts balance
# and never coverage: every package `go list` reports lands in exactly one
# shard.
race_cost() {
  case "$1" in
    */pkg/vfs/read) echo 26 ;;
    */internal/cli) echo 19 ;;
    */pkg/crypt) echo 18 ;;
    */pkg/drive | */pkg/vfs/upload) echo 12 ;;
    */pkg/vfs) echo 11 ;;
    */pkg/drivers/drime) echo 10 ;;
    */pkg/drivers/quark) echo 8 ;;
    */pkg/contracttest | */pkg/drivers/yun139) echo 7 ;;
    */pkg/drivers/p123 | */pkg/drivers/p189 | */pkg/drivers/s3 | */pkg/mount) echo 6 ;;
    */pkg/control | */pkg/drivers/koofr) echo 5 ;;
    */pkg/core | */pkg/mobile | */pkg/drivers/baidunetdisk) echo 4 ;;
    */pkg/vfs/readcache | */pkg/drivers/sftp) echo 3 ;;
    *) echo 2 ;;
  esac
}

# race_package_list prints the concrete race package list (one import path per
# line, `go list` order), so shard membership is stable for the same tree.
race_package_list() {
  go list "${RACE_PATTERNS[@]}" 2>/dev/null
}

# race_shard_plan <index> <count> reads "<cost> <import path>" lines and prints
# the import paths of one shard. Balance:
#   1. the four heavy packages are pinned one per shard first;
#   2. every other package is then packed cheapest-fit (most expensive first)
#      onto the least loaded shard.
# Ties resolve to the lowest shard index and the input order is the go list
# order, so the same tree always produces the same split.
race_shard_plan() {
  local index="$1" count="$2"
  awk -v idx="$index" -v n="$count" '
    function isheavy(p) {
      return p ~ /\/pkg\/vfs\/read$/ || p ~ /\/internal\/cli$/ ||
             p ~ /\/pkg\/crypt$/       || p ~ /\/pkg\/drive$/
    }
    function minload(   i, best) {
      best = 1
      for (i = 2; i <= n; i++) if (load[i] < load[best]) best = i
      return best
    }
    { total++; cost[total] = $1 + 0; pkg[total] = $2 }
    END {
      for (i = 1; i <= total; i++) order[i] = i
      # insertion sort: most expensive first, then import path for stable ties
      for (i = 2; i <= total; i++) {
        v = order[i]
        for (j = i - 1; j >= 1; j--) {
          if (cost[order[j]] > cost[v]) break
          if (cost[order[j]] == cost[v] && pkg[order[j]] < pkg[v]) break
          order[j + 1] = order[j]
        }
        order[j + 1] = v
      }
      # Heavy packages first: one per shard while shards are available, then
      # cheapest-fit so a shard count below four still yields a sane split.
      pinned = 0
      for (k = 1; k <= total; k++) {
        i = order[k]
        if (!isheavy(pkg[i])) continue
        if (pinned < n) {
          pinned++
          s = pinned
        } else {
          s = minload()
        }
        shard[i] = s
        load[s] += cost[i]
      }
      for (k = 1; k <= total; k++) {
        i = order[k]
        if (isheavy(pkg[i])) continue
        s = minload()
        shard[i] = s
        load[s] += cost[i]
      }
      for (i = 1; i <= total; i++) if (shard[i] == idx) print pkg[i]
    }
  '
}

# race_shard_packages <index> <count> prints the shard's import paths.
race_shard_packages() {
  local index="$1" count="$2" pkg
  {
    while IFS= read -r pkg; do
      [ -n "$pkg" ] || continue
      printf '%s %s\n' "$(race_cost "$pkg")" "$pkg"
    done < <(race_package_list)
  } | race_shard_plan "$index" "$count"
}

# run_race_shard <index> <count> runs one shard under -race. The package list is
# printed before the run so a CI failure names the exact subset, and the
# slowest-package/test report comes from the shared -json path.
run_race_shard() {
  local index="$1" count="$2" pkgs num
  pkgs=$(race_shard_packages "$index" "$count")
  if [ -z "$pkgs" ]; then
    printf '== race-shard %s/%s: no packages (shard count exceeds the race set) ==\n' "$index" "$count"
    return 0
  fi
  num=$(printf '%s\n' "$pkgs" | wc -l | tr -d ' ')
  printf '== race-shard %s/%s: %s package(s) ==\n' "$index" "$count" "$num"
  printf '%s\n' "$pkgs" | sed 's/^/     /'
  # The list is deliberately split into argv.
  # shellcheck disable=SC2086
  measure "race-shard $index/$count: go test -race ($num pkg)" \
    go test -json -race -count=1 -p "$(race_parallel)" $pkgs
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
      # The list is deliberately split into argv.
      # shellcheck disable=SC2086
      measure "changed: go test -json ($count pkg)" go test -json -count=1 $pkgs
    fi
    ;;
  race)
    # The whole pkg/vfs subtree, not just its main package: the read pipeline
    # and the readcache/scheduler helpers it drives are concurrency sensitive
    # -- the nightly prefetch flake lives in pkg/vfs/read -- and before this
    # change none of those packages ever ran under -race. The wildcard also
    # picks up subpackages added later. It is safe on every platform the layer
    # runs on: nothing under pkg/vfs depends on cgo/fuse, and the subtree
    # cross-lists cleanly for GOOS=windows. drivecopy needs no separate entry
    # now -- the wildcard covers it. race_parallel's 4x over-subscription was
    # tuned for timer/IO-bound suites; pkg/vfs/read is CPU-bound under -race,
    # so on a loaded 8-core box the default 32 in-flight packages cost wall
    # clock (76s, with pkg/vfs/read at 74s, versus 50s at RACE_P=8); on an
    # idle box p=16 and p=32 both take ~38s. RACE_P tunes that per runner
    # without changing concurrency coverage.
    measure "race: pkg/vfs/... drive drivers contracttest control logging mount syncer cli core mobile crypt task cmd" \
      go test -race -count=1 -p "$(race_parallel)" "${RACE_PATTERNS[@]}"
    ;;
  race-changed)
    # PR fast path: the same import-closure selection as `changed`, with the
    # reachable packages executed under -race. The fail-safe rule is the same
    # one `changed` documents: a diff that cannot be placed on packages
    # (scripts/, go.mod, the docs, no git checkout) widens to the full race set
    # rather than skipping concurrency coverage. -json keeps the slowest
    # package/test report.
    pkgs=$(affected_packages) || pkgs=""
    if [ -z "$pkgs" ]; then
      echo "== race-changed: diff cannot be placed on packages; running the full race set =="
      measure "race-changed: go test -race (full fallback)" \
        go test -json -race -count=1 -p "$(race_parallel)" "${RACE_PATTERNS[@]}"
    else
      count=$(printf '%s\n' "$pkgs" | wc -l | tr -d ' ')
      printf '== race-changed: %s package(s) reachable from the working diff ==\n' "$count"
      printf '%s\n' "$pkgs" | sed 's/^/     /'
      # The list is deliberately split into argv.
      # shellcheck disable=SC2086
      measure "race-changed: go test -race ($count pkg)" \
        go test -json -race -count=1 -p "$(race_parallel)" $pkgs
    fi
    ;;
  race-shard)
    spec="${2:-}"
    if [ "${spec#*/}" = "$spec" ] || [ -z "${spec%%/*}" ] || [ -z "${spec##*/}" ]; then
      echo "usage: $0 race-shard <index>/<count>" >&2
      exit 2
    fi
    index="${spec%%/*}"
    count="${spec##*/}"
    if ! [[ "$index" =~ ^[1-9][0-9]*$ ]] || ! [[ "$count" =~ ^[1-9][0-9]*$ ]]; then
      echo "usage: $0 race-shard <index>/<count>" >&2
      exit 2
    fi
    if [ "$index" -gt "$count" ]; then
      echo "race-shard: index $index is outside 1..$count" >&2
      exit 2
    fi
    run_race_shard "$index" "$count"
    ;;
  race-shard-all)
    count="${2:-4}"
    if ! [[ "$count" =~ ^[1-9][0-9]*$ ]]; then
      echo "usage: $0 race-shard-all [shard-count]" >&2
      exit 2
    fi
    # Local simulation of the nightly shard matrix. Each CI shard runs on its
    # own runner with the full package budget, but here all shards share one
    # machine, so halve the per-shard budget to keep the total in-flight work
    # near what a single `race` run asks for (oversubscribing makes the
    # timer-sensitive tests flake on a laptop). Set RACE_P to give every shard
    # an explicit budget instead.
    if [ -z "${RACE_P:-}" ]; then
      shard_p=$(( $(race_parallel) / (count * 2) ))
      if [ "$shard_p" -lt 2 ]; then shard_p=2; fi
      export RACE_P="$shard_p"
    fi
    tmp=$(mktemp -d)
    logs=()
    pids=()
    start=$(date +%s)
    for ((i = 1; i <= count; i++)); do
      logs[i]="$tmp/shard-$i.log"
      run_race_shard "$i" "$count" >"${logs[$i]}" 2>&1 &
      pids+=("$!")
    done
    status=0
    for pid in "${pids[@]}"; do
      wait "$pid" || status=1
    done
    for log in "${logs[@]}"; do
      cat "$log"
    done
    end=$(( $(date +%s) - start ))
    printf '== race-shard-all: %s shard(s) in %ds ==\n' "$count" "$end"
    grep -h '^== race-shard [0-9]*/[0-9]*: go test' "${logs[@]}" |
      sed 's/^== /     /; s/ ==$//' || true
    rm -rf "$tmp"
    exit "$status"
    ;;
  vfs-stability)
    # The async upload engine (worker shutdown, staging cleanup, journaling)
    # is timing sensitive; three independent runs catch the occasional flake.
    # The whole pkg/vfs subtree is repeated, not just the main package, so the
    # read pipeline and its prefetch/readcache/scheduler helpers -- where the
    # nightly prefetch flake lives -- are covered by the flake guard too. The
    # three runs are mostly timer-bound, so overlapping them costs much less
    # than running -count=3 per package.
    measure "vfs-stability: 3x go test -count=1 ./pkg/vfs/..." \
      run_repeated 3 go test -count=1 ./pkg/vfs/...
    ;;
  smoke)
    measure "smoke: localfs" scripts/ci/smoke-localfs.sh
    ;;
  all)
    measure "all: default suite" go test -count=1 ./...
    measure "all: race vfs+core" go test -race -count=1 ./pkg/vfs ./pkg/core
    measure "all: localfs smoke" scripts/ci/smoke-localfs.sh
    ;;
  integration)
    # Requires a running debug server with real cloud mounts; see the
    # contract-test comment above. Each mount runs the full CRUD/contract
    # suite, so pass only the mounts you actually have configured.
    : "${QRYPT_CONTRACT_CONFIG:?integration layer needs QRYPT_CONTRACT_CONFIG (path to qrypt.toml)}"
    mounts=("$@")
    if [ "${#mounts[@]}" -le 1 ]; then
      echo "usage: $0 integration <mount> [mount...]" >&2
      exit 2
    fi
    for m in "${mounts[@]:1}"; do
      measure "integration: contract $m" \
        go run ./cmd/qrypt probe test contract --config "$QRYPT_CONTRACT_CONFIG" --mount "$m"
    done
    ;;
  *)
    echo "usage: $0 [fast|changed|race|race-changed|race-shard <index>/<count>|race-shard-all [count]|vfs-stability|smoke|all|integration <mount>...]" >&2
    exit 2
    ;;
esac
