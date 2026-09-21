#!/usr/bin/env python3
"""Keep t.Parallel() on exactly the tests that can have it.

Two kinds of process-wide state make a test unsafe to run alongside others, and
neither is visible from the test body alone:

  * Calls the testing package rejects once t.Parallel() has been called
    (t.Setenv, t.Chdir), calls that mutate the process environment or working
    directory, and goleak.Find -- which inspects every goroutine in the process
    and so never settles while another test is running.
  * A production package-level variable used as a test seam: core's
    UploadStreamTaskPollInterval and DirectUploadRetryBaseDelay, vfs/upload's
    logging.L. Whichever test is running writes it, so two of them race on the
    assignment itself.

Both propagate through helpers: newTaskBoundaryCore in pkg/core writes a seam,
so every test calling it has to stay serial even though its own body never
mentions one. The call graph is followed to a fixpoint for that reason.

`apply` puts t.Parallel() on the tests that can take it and takes it back off
the ones above. `audit` reports the same findings without writing and exits 1
when anything would change, so it can be used as a gate. Both are idempotent,
so re-running after adding tests converges.

Why marking the writers serial is enough for the readers: go's testing package
runs every test that does not call t.Parallel() to completion before it resumes
the ones that do, so a serial writer always finishes before a parallel reader
starts. That is what keeps pkg/core race-clean with only its 13 writers serial.

What this cannot see, and -race has to catch:

  * a test that depends on another test having left a seam in a particular
    state. pkg/mobile's TestMobileCreateDirectUploadTaskJSON only reads
    uploadSourceOpener, but it needs the opener another test installs, so
    parallelising it fails on ordering rather than on a race. Nothing in its
    body says so.
  * reads that reach a seam without naming it or calling a local seam writer.

So the workflow is `apply`, then `test-layers.sh race` -- not apply alone. The
race layer is what turned up all nine of the assignments this script now marks
serial in the first place.

Usage:
  scripts/test-parallelism.py audit PKGDIR...
  scripts/test-parallelism.py apply PKGDIR...
"""

import re
import sys
from pathlib import Path

TEST_SIG = re.compile(r"^func (Test[A-Za-z0-9_]*)\(t \*testing\.T\) \{$")
FUNC_SIG = re.compile(r"^func ([A-Za-z_][A-Za-z0-9_]*)\s*\(")
VAR_DECL = re.compile(r"^var ([A-Za-z_][A-Za-z0-9_]*)\b")
CALL = re.compile(r"\b([A-Za-z_][A-Za-z0-9_]*)\s*\(")
# needle in a function body, and why it forces that function to stay serial.
DIRECT_BLOCKERS = (
    ("t.Setenv(", "testing rejects t.Setenv after t.Parallel"),
    ("t.Chdir(", "testing rejects t.Chdir after t.Parallel"),
    ("os.Setenv(", "mutates the process environment"),
    ("os.Chdir(", "mutates the process working directory"),
    ("goleak.Find(", "goleak.Find inspects every goroutine in the process"),
)


def package_vars(pkgdir):
    """Names declared at package level in the package's non-test files.

    Blank `_` is dropped: it is not a variable, and `_ = f()` is everywhere in
    test bodies, so keeping it would flag most of the package.
    """
    names = set()
    in_block = False
    for path in sorted(Path(pkgdir).glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        for line in path.read_text().split("\n"):
            if in_block:
                if line.startswith(")"):
                    in_block = False
                else:
                    m = re.match(r"^\t([A-Za-z_][A-Za-z0-9_]*)\s", line)
                    if m:
                        names.add(m.group(1))
                continue
            if line.startswith("var ("):
                in_block = True
                continue
            m = VAR_DECL.match(line)
            if m:
                names.add(m.group(1))
    return names - {"_"}


def body_of(lines, start):
    """Body lines of the function opening at `start`, up to its closing brace.

    gofmt puts a top-level function's closing brace and every following
    top-level declaration at column 0, so no brace counting is needed -- which
    braces inside string literals and comments would defeat anyway.
    """
    body = []
    i = start
    while i < len(lines):
        if lines[i].startswith("}") or lines[i].startswith("func "):
            break
        body.append(lines[i])
        i += 1
    return body


def module_path(root):
    """The module path declared in go.mod, or None."""
    for line in (root / "go.mod").read_text().split("\n"):
        if line.startswith("module "):
            return line.split()[1]
    return None


def imports_of(lines):
    """alias -> import path for a file's imports.

    Imports sit at the top, but the whole file is scanned so a stray later
    `import (` cannot be missed for a few lines' worth of saving.
    """
    text = "\n".join(lines)
    found = {}
    blocks = re.search(r"^import \(\n(.*?)^\)$", text, re.M | re.S)
    entries = [e for e in (blocks.group(1).split("\n") if blocks else []) if e.strip()]
    entries += [f'import "{p}"' if not a else f'import {a} "{p}"'
                for a, p in re.findall(r'^import\s+(?:(\w+)\s+)?"([^"]+)"$', text, re.M)]

    for entry in entries:
        m = re.match(r'^(?:(\w+|\.|_)\s+)?"([^"]+)"', entry.strip().removeprefix("import ").strip())
        if not m or m.group(1) == "_":
            continue
        alias, path = m.group(1), m.group(2)
        found[alias or path.rsplit("/", 1)[-1]] = path
    return found


def seam_writers(pkgdir, var_cache):
    """Production functions in pkgdir that assign a package-level variable.

    These are the seams behind an accessor: pkg/mobile's SetUploadSourceOpenerJSON
    writes uploadSourceOpener, so a test calling it is as unsafe to parallelise
    as one assigning the variable itself -- and no test body names the variable.
    Returns name -> the variable it writes.
    """
    key = ("writers", pkgdir)
    if key in var_cache:
        return var_cache[key]

    names = var_cache.setdefault(pkgdir, package_vars(pkgdir))
    writers = {}
    if names:
        assigns = re.compile(
            r"^\s*(" + "|".join(sorted(map(re.escape, names))) + r")\s*=[^=]", re.M
        )
        for path in sorted(pkgdir.glob("*.go")):
            if path.name.endswith("_test.go"):
                continue
            lines = path.read_text().split("\n")
            for i, line in enumerate(lines):
                m = FUNC_SIG.match(line)
                if not m:
                    continue
                hits = assigns.findall("\n".join(body_of(lines, i + 1)))
                if hits:
                    writers[m.group(1)] = f"writes package-level {sorted(set(hits))[0]}"
    var_cache[key] = writers
    return writers


def state_patterns(lines, root, module, pkgdir, var_cache):
    """Patterns for process-wide state the test's own package does not own.

    Three shapes the body-only scan misses: a call to one of this package's
    production seam writers, an assignment to an imported package's
    package-level variable (pkg/vfs/upload's store_scope_test writes
    pkg/logging's logging.L), and a call to an imported package's seam writer.
    One pattern per name keeps every reason a fixed string.
    """
    patterns = [
        (re.compile(r"\b" + re.escape(name) + r"\s*\("), f"calls {name}() which {reason}")
        for name, reason in seam_writers(pkgdir, var_cache).items()
    ]

    for alias, path in imports_of(lines).items():
        if not path.startswith(module + "/"):
            continue
        imported = root / path[len(module) + 1 :]
        if not imported.is_dir():
            continue

        for name in var_cache.setdefault(imported, package_vars(imported)):
            patterns.append((
                re.compile(r"^\s*" + re.escape(alias) + r"\." + re.escape(name) + r"\s*=[^=]", re.M),
                f"writes package-level {alias}.{name}",
            ))
        for name, reason in seam_writers(imported, var_cache).items():
            patterns.append((
                re.compile(r"\b" + re.escape(alias) + r"\." + re.escape(name) + r"\s*\("),
                f"calls {alias}.{name}() which {reason}",
            ))
    return patterns


def scan(lines, seam_names, extra_patterns):
    """Every top-level function in a test file, with what it does directly."""
    assigns = (
        re.compile(r"^\s*(" + "|".join(sorted(map(re.escape, seam_names))) + r")\s*=[^=]", re.M)
        if seam_names
        else None
    )

    found = []
    for i, line in enumerate(lines):
        is_test = TEST_SIG.match(line) is not None
        if not is_test and not FUNC_SIG.match(line):
            continue
        sig = TEST_SIG if is_test else FUNC_SIG
        text = "\n".join(body_of(lines, i + 1))
        has_parallel = i + 1 < len(lines) and lines[i + 1].strip() == "t.Parallel()"

        reasons = [why for needle, why in DIRECT_BLOCKERS if needle in text]
        reasons += [f"writes package-level {s}" for s in sorted(set(assigns.findall(text)))] if assigns else []
        for pattern, reason in extra_patterns:
            if pattern.search(text):
                reasons.append(reason)

        found.append(
            {
                "name": sig.match(line).group(1),
                "is_test": is_test,
                "parallel_after": i + 1 if has_parallel else -1,
                "reasons": reasons,
                "calls": set(CALL.findall(text)),
            }
        )
    return found


def serial_reasons(funcs):
    """Map function name -> reasons it must stay serial, following calls.

    A test calling a helper that writes a seam is as unsafe as one writing it
    directly, so reasons propagate up the call graph until they settle.
    """
    by_name = {f["name"]: f for f in funcs}
    reasons = {f["name"]: list(f["reasons"]) for f in funcs}

    changed = True
    while changed:
        changed = False
        for f in funcs:
            for callee in f["calls"]:
                if callee not in by_name or callee == f["name"]:
                    continue
                for reason in reasons[callee]:
                    via = f"via {callee}(): {reason}"
                    if via not in reasons[f["name"]]:
                        reasons[f["name"]].append(via)
                        changed = True
    return reasons


def plan_file(lines, seam_names, extra_patterns):
    """Desired contents of one test file, plus the findings that produced them."""
    reasons = serial_reasons(scan(lines, seam_names, extra_patterns))

    out, dropped, findings = [], set(), []
    for i, line in enumerate(lines):
        if i in dropped:
            continue
        out.append(line)
        m = TEST_SIG.match(line)
        if not m:
            continue
        name = m.group(1)
        has_parallel = i + 1 < len(lines) and lines[i + 1].strip() == "t.Parallel()"
        if reasons.get(name):
            if has_parallel:
                dropped.add(i + 1)
                findings.append(("remove", name, reasons[name][0]))
        elif not has_parallel:
            out.append("\tt.Parallel()")
            findings.append(("add", name, ""))

    return out, findings


def main():
    if len(sys.argv) < 3 or sys.argv[1] not in ("audit", "apply"):
        print("usage: test-parallelism.py audit|apply PKGDIR...", file=sys.stderr)
        return 2
    mode, dirs = sys.argv[1], sys.argv[2:]
    root = Path(__file__).resolve().parent.parent
    module = module_path(root)
    if module is None:
        print("test-parallelism.py: no module line in go.mod", file=sys.stderr)
        return 2

    var_cache = {}
    total = 0
    for pkgdir in dirs:
        seam_names = package_vars(Path(pkgdir))
        for path in sorted(Path(pkgdir).glob("*_test.go")):
            lines = path.read_text().split("\n")
            extra = state_patterns(lines, root, module, Path(pkgdir), var_cache)
            out, findings = plan_file(lines, seam_names, extra)
            for action, name, reason in findings:
                suffix = f" -- {reason}" if reason else ""
                print(f"  {pkgdir}: {action} t.Parallel() on {name}{suffix}")
            total += len(findings)
            if mode == "apply" and findings:
                path.write_text("\n".join(out))

    print(f"{mode}: {total} finding(s)")
    return 1 if (mode == "audit" and total) else 0


if __name__ == "__main__":
    sys.exit(main())
