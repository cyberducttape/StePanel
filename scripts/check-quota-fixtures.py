#!/usr/bin/env python3
"""Ensure quota-enabled host fixtures can admit the largest Git staging job."""

from pathlib import Path
import re
import sys


ROOT = Path(__file__).resolve().parent.parent
GIB = 1 << 30


def required_capacity() -> int:
    config = (ROOT / "config.go").read_text(encoding="utf-8")
    installer = (ROOT / "install.sh").read_text(encoding="utf-8")
    tree_match = re.search(r"GitReleaseMaxTreeBytes:\s*(\d+)\s*<<\s*30", config)
    free_match = re.search(r'MIN_FREE_BYTES="\$\{STEPANEL_MIN_FREE_BYTES:-(\d+)\}"', installer)
    if not tree_match or not free_match:
        raise ValueError("cannot determine default Git staging or minimum-free reservation")
    return int(tree_match.group(1)) * GIB + int(free_match.group(1))


def fixture_capacity(path: Path) -> int:
    text = path.read_text(encoding="utf-8")
    match = re.search(r'qemu-img create(?: -q)? -f raw [^\n]*www\.ext4"\s+(\d+)([GM])\b', text)
    if not match:
        raise ValueError(f"{path.relative_to(ROOT)} has no recognizable /var/www quota volume")
    amount = int(match.group(1))
    return amount * (GIB if match.group(2) == "G" else 1 << 20)


def main() -> int:
    required = required_capacity()
    fixtures = (
        ROOT / ".github/workflows/quota-install-smoke.yml",
        ROOT / "deploy/lab/local-kvm-certification.sh",
    )
    for fixture in fixtures:
        actual = fixture_capacity(fixture)
        if actual < required:
            print(
                f"{fixture.relative_to(ROOT)} /var/www quota volume is {actual} bytes; "
                f"need at least {required} for Git staging plus minimum-free space",
                file=sys.stderr,
            )
            return 1
        print(f"{fixture.relative_to(ROOT)}: {actual} bytes >= {required} required")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
