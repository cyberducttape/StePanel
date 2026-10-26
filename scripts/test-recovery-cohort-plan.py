#!/usr/bin/env python3
"""Contract tests for recovery cohort sharding and source pinning."""

import importlib.util
import json
import sys
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("plan-recovery-cohort.py")
SPEC = importlib.util.spec_from_file_location("plan_recovery_cohort", SCRIPT)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
sys.dont_write_bytecode = True
SPEC.loader.exec_module(MODULE)


class RecoveryCohortPlanTests(unittest.TestCase):
    def test_plans_minimum_and_maximum_cohorts_with_unique_run_scoped_shards(self):
        source = "a" * 40
        for count in (20, 50):
            with self.subTest(count=count):
                plan = MODULE.plan_recovery_cohort(source, "40330000001", str(count))
                matrix = json.loads(plan["matrix"])
                shards = [entry["shard"] for entry in matrix["include"]]
                self.assertEqual(count, len(shards))
                self.assertEqual(count, len(set(shards)))
                self.assertEqual("gate5-40330000001-01", shards[0])
                self.assertEqual(source, plan["source_sha"])
                self.assertEqual(count, plan["repetitions"])

    def test_rejects_non_release_cohort_sizes(self):
        for count in ("0", "1", "19", "51", "100", "20.0", ""):
            with self.subTest(count=count), self.assertRaises(ValueError):
                MODULE.plan_recovery_cohort("a" * 40, "1", count)

    def test_rejects_unpinned_or_malformed_source_and_run_identity(self):
        for source in ("main", "a" * 39, "A" * 40):
            with self.subTest(source=source), self.assertRaises(ValueError):
                MODULE.plan_recovery_cohort(source, "1", "20")
        for run_id in ("", "latest", "1/2"):
            with self.subTest(run_id=run_id), self.assertRaises(ValueError):
                MODULE.plan_recovery_cohort("a" * 40, run_id, "20")


if __name__ == "__main__":
    unittest.main()
