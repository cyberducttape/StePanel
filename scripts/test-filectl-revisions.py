#!/usr/bin/env python3
"""Exercise the descriptor-rooted file helper's conditional-write contract."""
import hashlib
import pathlib
import re
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
SOURCE = (ROOT / "deploy/integrations/stepanel-filectl").read_text(encoding="utf-8")
match = re.search(r"filectl_python=\$\(cat <<'PY'\n(.*?)\nPY\n\)", SOURCE, re.S)
if not match:
    raise SystemExit("could not find embedded file helper Python source")
HELPER = match.group(1)

MAX_READ = 2 * 1024 * 1024
MAX_WRITE = 4 * 1024 * 1024


def revision(data: bytes) -> str:
    return '"sha256-' + hashlib.sha256(data).hexdigest() + '"'


def write(root: pathlib.Path, expected: str, body: bytes) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, "-c", HELPER, "write", str(root), "page.php", ".", str(MAX_READ), str(MAX_WRITE), "5000", expected],
        input=body,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )


with tempfile.TemporaryDirectory(prefix="stepanel-file-revision-") as directory:
    root = pathlib.Path(directory)
    target = root / "page.php"
    target.write_bytes(b"newer saved version")

    stale = write(root, revision(b"version when editor opened"), b"stale overwrite")
    if stale.returncode == 0 or b"file changed since it was opened" not in stale.stderr:
        raise SystemExit(f"stale conditional write was not rejected: {stale.returncode} {stale.stderr!r}")
    if target.read_bytes() != b"newer saved version":
        raise SystemExit("stale conditional write replaced the current file")

    accepted = write(root, revision(target.read_bytes()), b"accepted update")
    if accepted.returncode != 0 or target.read_bytes() != b"accepted update":
        raise SystemExit(f"matching conditional write failed: {accepted.returncode} {accepted.stderr!r}")

    oversized = b"x" * (MAX_READ + 1)
    target.write_bytes(oversized)
    refused_large = write(root, revision(oversized), b"must not replace large file")
    if refused_large.returncode == 0 or b"file changed since it was opened" not in refused_large.stderr:
        raise SystemExit("conditional revision check did not reject an oversized current file")
    if target.stat().st_size != len(oversized):
        raise SystemExit("oversized current file was replaced")

print("file helper conditional revision contract passed")
