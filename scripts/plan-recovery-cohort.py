#!/usr/bin/env python3
"""Create unique, exact-run recovery shard identifiers for GitHub Actions."""

from __future__ import annotations

import json
import re
import sys


SHA_PATTERN = re.compile(r"^[0-9a-f]{40}$")
RUN_ID_PATTERN = re.compile(r"^[0-9]+$")


def plan_recovery_cohort(source_sha: str, run_id: str, repetitions: str) -> dict[str, object]:
    if not SHA_PATTERN.fullmatch(source_sha):
        raise ValueError("source SHA must be a full lowercase 40-character commit hash")
    if not RUN_ID_PATTERN.fullmatch(run_id):
        raise ValueError("GitHub run ID must contain decimal digits only")
    if not repetitions.isdecimal():
        raise ValueError("repetitions must be an integer from 20 through 50")
    count = int(repetitions)
    if not 20 <= count <= 50:
        raise ValueError("repetitions must be an integer from 20 through 50")

    shards = [f"gate5-{run_id}-{index:02d}" for index in range(1, count + 1)]
    if len(set(shards)) != count or any(len(shard) > 40 for shard in shards):
        raise ValueError("generated recovery shard identifiers are invalid")
    return {
        "matrix": json.dumps({"include": [{"shard": shard} for shard in shards]}, separators=(",", ":")),
        "source_sha": source_sha,
        "repetitions": count,
    }


def main(argv: list[str]) -> int:
    if len(argv) != 4:
        print("usage: plan-recovery-cohort.py SOURCE_SHA RUN_ID REPETITIONS", file=sys.stderr)
        return 64
    try:
        plan = plan_recovery_cohort(argv[1], argv[2], argv[3])
    except ValueError as exc:
        print(f"invalid recovery cohort: {exc}", file=sys.stderr)
        return 64
    for key, value in plan.items():
        print(f"{key}={value}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
