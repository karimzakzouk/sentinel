#!/usr/bin/env python3
"""
sync-version.py — single source of truth for Sentinel versioning.

The VERSION file (e.g. `4.4.0`) is canonical. This script reads it and
rewrites every other place the version appears so they all match.

Usage:
    python3 scripts/sync-version.py            # sync everything to VERSION file
    python3 scripts/sync-version.py --check    # exit 1 if drift detected (CI mode)
    python3 scripts/sync-version.py --bump 4.5.0   # set VERSION + sync everything

Files synced:
    VERSION                     — canonical
    chart/Chart.yaml            — version + appVersion
    chart/values.yaml           — image.tag + comment header
    my-values.yaml              — image.tag
    web/package.json            — version field
    README.md                   — `helm install ... --version X.X.X` command
    internal/version/version.go — Version constant (string literal)

After running this, `git diff` will show all files that drifted. Commit them.

The CI workflow (.github/workflows/ci.yml) runs `--check` on every PR to
fail the build if any file is out of sync with VERSION.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent

# Files + how to update them.
# Each entry: (relative_path, kind, optional context)
SYNC_TARGETS = [
    ("VERSION",                       "raw",        None),
    ("chart/Chart.yaml",              "chart_yaml", None),
    ("chart/values.yaml",             "values_tag", None),
    ("chart/values.yaml",             "comment_ver", None),
    ("my-values.yaml",                "values_tag", None),
    ("web/package.json",              "pkg_json",   None),
    ("README.md",                     "readme_install", None),
    ("internal/version/version.go",   "go_const",   None),
]


def read_version() -> str:
    return (REPO / "VERSION").read_text().strip()


def write_version(v: str) -> None:
    (REPO / "VERSION").write_text(v + "\n")


def patch(path: Path, pattern: str, repl: str, *, check: bool) -> bool:
    """Replace `pattern` (regex) with `repl` in `path`. Returns True if file
    was changed (or, in check mode, returns True if drift detected)."""
    src = path.read_text()
    new = re.sub(pattern, repl, src, count=1, flags=re.MULTILINE)
    if new == src:
        return False
    if not check:
        path.write_text(new)
    return True


def sync_one(file_rel: str, kind: str, version: str, *, check: bool) -> tuple[bool, str]:
    """Returns (was_changed_or_drifted, human_message)."""
    p = REPO / file_rel
    if not p.exists():
        return False, f"  SKIP {file_rel} (not found)"

    if kind == "raw":
        cur = p.read_text().strip()
        if cur != version:
            if not check:
                p.write_text(version + "\n")
            return True, f"  {'DRIFT' if check else 'OK  '} {file_rel}: {cur} → {version}"
        return False, f"  OK   {file_rel} (already {version})"

    if kind == "chart_yaml":
        changed_v = patch(p, r'^(version:\s*).*$', rf'\g<1>{version}', check=check)
        changed_av = patch(p, r'^(appVersion:\s*)".*"$', rf'\g<1>"{version}"', check=check)
        if changed_v or changed_av:
            return True, f"  {'DRIFT' if check else 'OK  '} {file_rel}: version + appVersion → {version}"
        return False, f"  OK   {file_rel} (already {version})"

    if kind == "values_tag":
        # matches `  tag: "X.X.X"` (chart/values.yaml + my-values.yaml)
        changed = patch(p, r'^(\s*tag:\s*)"[^"]*"$', rf'\g<1>"{version}"', check=check)
        if changed:
            return True, f"  {'DRIFT' if check else 'OK  '} {file_rel}: image.tag → {version}"
        return False, f"  OK   {file_rel} (already {version})"

    if kind == "comment_ver":
        # matches `--version X.X.X` in comment headers
        changed = patch(p, r'(helm install sentinel sentinel/sentinel --version )[\d.]+', rf'\g<1>{version}', check=check)
        if changed:
            return True, f"  {'DRIFT' if check else 'OK  '} {file_rel}: comment --version → {version}"
        return False, f"  OK   {file_rel} (comment already {version})"

    if kind == "pkg_json":
        data = json.loads(p.read_text())
        if data.get("version") != version:
            if not check:
                data["version"] = version
                p.write_text(json.dumps(data, indent=2) + "\n")
            return True, f"  {'DRIFT' if check else 'OK  '} {file_rel}: {data.get('version')} → {version}"
        return False, f"  OK   {file_rel} (already {version})"

    if kind == "readme_install":
        # README install command — `--version 3.17.0`
        changed = patch(p, r'(helm install sentinel sentinel/sentinel --version )[\d.]+', rf'\g<1>{version}', check=check)
        if changed:
            return True, f"  {'DRIFT' if check else 'OK  '} {file_rel}: install --version → {version}"
        return False, f"  OK   {file_rel} (install cmd already {version})"

    if kind == "go_const":
        # internal/version/version.go: var Version = "dev" (default) or "X.Y.Z" (set by ldflags)
        # We don't actually set the version in source — ldflags injects it at build time.
        # The source file always says "dev". So this file never drifts.
        return False, f"  OK   {file_rel} (ldflags-injected at build time)"

    return False, f"  SKIP {file_rel} (unknown kind {kind})"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--check", action="store_true", help="Exit 1 if any drift detected (CI mode)")
    ap.add_argument("--bump", metavar="VERSION", help="Set VERSION to X.Y.Z and sync everything")
    args = ap.parse_args()

    if args.bump:
        if not re.fullmatch(r'\d+\.\d+\.\d+', args.bump):
            print(f"ERROR: invalid version '{args.bump}' (expected X.Y.Z)", file=sys.stderr)
            return 2
        write_version(args.bump)
        print(f"VERSION file set to {args.bump}")

    version = read_version()
    mode = "CHECK" if args.check else "SYNC"
    print(f"\n{mode} → {version}\n" + "=" * 40)

    drift_count = 0
    for file_rel, kind, _ in SYNC_TARGETS:
        changed, msg = sync_one(file_rel, kind, version, check=args.check)
        print(msg)
        if changed:
            drift_count += 1

    print("=" * 40)
    if args.check:
        if drift_count > 0:
            print(f"FAIL: {drift_count} file(s) drifted from VERSION={version}")
            print("Run: python3 scripts/sync-version.py")
            return 1
        print(f"OK: all files in sync with VERSION={version}")
        return 0
    if drift_count > 0:
        print(f"Synced {drift_count} file(s) to VERSION={version}")
    else:
        print(f"All files already in sync with VERSION={version}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
