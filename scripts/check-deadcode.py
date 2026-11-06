#!/usr/bin/env python3
"""Reject new unreachable Go functions against an explicit reviewed baseline."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

VERSION = "v0.49.0"
BASELINE = "scripts/deadcode-baseline.json"


def run(args, *, cwd, env=None):
    return subprocess.check_output(args, cwd=cwd, env=env, text=True)


def relevant(path):
    return (path.endswith((".go", ".s", ".syso")) or
            path in {"go.mod", "go.sum", BASELINE, "scripts/check-deadcode.py",
                     ".githooks/pre-commit"})


def tool(env):
    # Build the tool for the host; analyze the deployment target separately.
    cache = Path(env.get("XDG_CACHE_HOME", Path.home() / ".cache"))
    bindir = cache / "gopod-tools" / ("deadcode-" + VERSION)
    binary = bindir / "deadcode"
    if not binary.is_file():
        bindir.mkdir(parents=True, exist_ok=True)
        install_env = dict(env, GOBIN=str(bindir.resolve()), GOWORK="off", GOFLAGS="")
        install_env.pop("GOOS", None)
        install_env.pop("GOARCH", None)
        print("Installing pinned deadcode " + VERSION, flush=True)
        subprocess.run(["go", "install", "golang.org/x/tools/cmd/deadcode@" + VERSION],
                       env=install_env, check=True)
    return str(binary.resolve())


def analyze(root, binary, env):
    baseline = json.loads((root / BASELINE).read_text())
    if not isinstance(baseline, list) or not all(isinstance(x, str) for x in baseline):
        raise ValueError("deadcode baseline must be a JSON list of symbol names")
    if len(baseline) != len(set(baseline)):
        raise ValueError("duplicate deadcode baseline entries")
    target = dict(env, GOOS="linux", GOARCH="arm64", CGO_ENABLED="0",
                  GOWORK="off", GOFLAGS="")
    # Tests are roots too: planned/test-only APIs are not blanket deletion targets.
    output = run([binary, "-test", "-json", "./..."], cwd=root, env=target)
    packages = json.loads(output)
    found = {p["Path"] + "." + f["Name"]
             for p in (packages or []) for f in p.get("Funcs", [])}
    new = sorted(found - set(baseline))
    stale = sorted(set(baseline) - found)
    for name in new:
        print("NEW unreachable: " + name, file=sys.stderr)
    for name in stale:
        print("STALE baseline (remove entry): " + name, file=sys.stderr)
    if new or stale:
        print("Review each symbol; do not automatically extend the baseline.", file=sys.stderr)
        return 1
    print(f"deadcode: PASS (linux/arm64, tests included, {len(found)} baseline candidates)")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--staged", action="store_true", help="analyze only the index snapshot")
    args = parser.parse_args()
    root = Path(run(["git", "rev-parse", "--show-toplevel"], cwd=Path.cwd()).strip())
    if args.staged:
        changed = run(["git", "diff", "--cached", "--no-renames", "--name-only", "-z"], cwd=root).split("\0")
        if not any(relevant(p) for p in changed):
            print("deadcode: skipped (no staged Go/checker changes)")
            return 0
    env = os.environ.copy()
    binary = tool(env)
    if not args.staged:
        return analyze(root, binary, env)
    # No working-tree overlay: unstaged callers cannot hide staged dead code.
    with tempfile.TemporaryDirectory(prefix="gopod-deadcode-index-") as directory:
        snapshot = Path(directory)
        subprocess.run(["git", "checkout-index", "--all", "--prefix=" + directory + os.sep],
                       cwd=root, check=True)
        return analyze(snapshot, binary, env)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print("deadcode check failed: " + str(error), file=sys.stderr)
        sys.exit(1)
