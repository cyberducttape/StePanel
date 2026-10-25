#!/usr/bin/env python3
"""Validate quota-enabled host fixture capacity and ext4 setup syntax."""

from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parent.parent
GIB = 1 << 30


def required_capacity() -> int:
    config = (ROOT / "config.go").read_text(encoding="utf-8")
    installer = (ROOT / "install.sh").read_text(encoding="utf-8")
    git_deploy = (ROOT / "git_deploy.go").read_text(encoding="utf-8")
    tree_match = re.search(r"GitReleaseMaxTreeBytes:\s*(\d+)\s*<<\s*30", config)
    free_match = re.search(r'MIN_FREE_BYTES="\$\{STEPANEL_MIN_FREE_BYTES:-(\d+)\}"', installer)
    # Git staging reserves the aggregate clone quota, a multiple of the tree limit.
    multiplier_match = re.search(r"gitCloneStageMultiplier\s*=\s*uint64\((\d+)\)", git_deploy)
    if not tree_match or not free_match or not multiplier_match:
        raise ValueError("cannot determine default Git staging or minimum-free reservation")
    return int(tree_match.group(1)) * int(multiplier_match.group(1)) * GIB + int(free_match.group(1))


def fixture_capacity(path: Path) -> int:
    text = path.read_text(encoding="utf-8")
    match = re.search(r'qemu-img create(?: -q)? -f raw [^\n]*www\.(?:ext4|quota)"\s+(\d+)([GM])\b', text)
    if not match:
        raise ValueError(f"{path.relative_to(ROOT)} has no recognizable /var/www quota volume")
    amount = int(match.group(1))
    return amount * (GIB if match.group(2) == "G" else 1 << 20)


def validate_ext4_quota_setup(path: Path) -> None:
    text = path.read_text(encoding="utf-8")
    commands = [
        line.strip()
        for line in text.splitlines()
        if "mkfs.ext4" in line and "/dev/vdb" in line
    ]
    if len(commands) != 1:
        raise ValueError(
            f"{path.relative_to(ROOT)} must define exactly one /dev/vdb ext4 formatter"
        )
    command = commands[0]
    features = re.search(r"(?:^|\s)-O\s+([^\s]+)", command)
    quota_types = re.search(r"(?:^|\s)-E\s+quotatype=([^\s]+)", command)
    enabled_features = set(features.group(1).split(",")) if features else set()
    if not {"project", "quota"}.issubset(enabled_features):
        raise ValueError(
            f"{path.relative_to(ROOT)} must enable ext4 project and quota features"
        )
    if not quota_types or set(quota_types.group(1).split(":")) != {"usrquota", "prjquota"}:
        raise ValueError(
            f"{path.relative_to(ROOT)} must use colon-separated usrquota:prjquota types"
        )


def validate_kvm_home_quota_setup(path: Path) -> None:
    text = path.read_text(encoding="utf-8")
    required = (
        "mkfs.ext4 -F -O project,quota -E quotatype=usrquota:prjquota /dev/vdd",
        "'/dev/vdd /home ext4 usrquota,prjquota 0 2'",
        "    mount /home",
        "quotaon -p -u /home | grep",
        "quotaon -p -P /home | grep",
    )
    missing = [fragment for fragment in required if fragment not in text]
    if missing:
        raise ValueError(
            f"{path.relative_to(ROOT)} lacks verified /home user/project quotas: "
            f"{', '.join(missing)}"
        )
    if "quotaon -u /home" in text or "quotaon -P /home" in text:
        raise ValueError(
            f"{path.relative_to(ROOT)} redundantly activates quotas already enabled by the /home mount options"
        )


def validate_xfs_quota_setup(path: Path) -> None:
    text = path.read_text(encoding="utf-8")
    required = (
        "          - xfs",
        "mkfs.xfs -f /dev/vdb",
        "xfs_quota -x -c 'state -up' /var/www",
        "xfs_quota -x -c 'state -u' /home",
    )
    missing = [fragment for fragment in required if fragment not in text]
    if missing:
        raise ValueError(
            f"{path.relative_to(ROOT)} lacks XFS quota coverage: {', '.join(missing)}"
        )

    # cloud-init runs runcmd scripts through /bin/sh, not the host workflow's
    # Bash. Extract and syntax-check the guest script so Bash-only conditionals
    # cannot silently select the wrong filesystem formatter.
    lines = text.splitlines()
    try:
        start = lines.index("          runcmd:") + 2
        end = lines.index("          EOF", start)
    except ValueError as exc:
        raise ValueError("cannot locate quota guest runcmd script") from exc
    guest_lines = lines[start:end]
    guest_script = "\n".join(
        line[14:] if line.startswith("              ") else line
        for line in guest_lines
    )
    # The workflow writes this with an unquoted host-side heredoc. Escaped
    # dollar signs and command substitutions are intentionally preserved for
    # the guest shell; unescape them here before checking the rendered script.
    guest_script = guest_script.replace(r"\$", "$")
    if "[[" in guest_script:
        raise ValueError("quota guest runcmd uses Bash-only [[ under cloud-init /bin/sh")
    if guest_script.count("if [ '$quota_filesystem' = xfs ]; then") != 4:
        raise ValueError("quota guest runcmd must select XFS in all four POSIX branches")
    result = subprocess.run(
        ["sh", "-n"], input=guest_script, text=True, capture_output=True, check=False
    )
    if result.returncode:
        raise ValueError(f"quota guest runcmd is not POSIX-shell syntax: {result.stderr.strip()}")


def main() -> int:
    required = required_capacity()
    fixtures = (
        ROOT / ".github/workflows/quota-install-smoke.yml",
        ROOT / "deploy/lab/local-kvm-certification.sh",
    )
    for fixture in fixtures:
        validate_ext4_quota_setup(fixture)
        if fixture.name == "quota-install-smoke.yml":
            validate_xfs_quota_setup(fixture)
        else:
            validate_kvm_home_quota_setup(fixture)
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
