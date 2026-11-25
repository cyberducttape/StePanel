"""Fast tests for the capacity probe's bounded HTTP diagnostics."""

import importlib.util
import unittest
from pathlib import Path
from unittest.mock import patch


MODULE_PATH = Path(__file__).with_name("capacity-profile.py")
SPEC = importlib.util.spec_from_file_location("capacity_profile", MODULE_PATH)
capacity_profile = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(capacity_profile)


class FakeTLS:
    def __init__(self, response):
        self.response = response

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return None

    def sendall(self, _data):
        pass

    def recv(self, size):
        chunk, self.response = self.response[:size], self.response[size:]
        return chunk


class FakeContext:
    check_hostname = True
    verify_mode = None

    def wrap_socket(self, _raw, server_hostname):
        return FakeTLS(self.response)

    def __init__(self, response):
        self.response = response


class FakeRaw:
    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return None


class CapacityProbeTest(unittest.TestCase):
    def probe(self, response):
        context = FakeContext(response)
        with patch.object(capacity_profile.ssl, "create_default_context", return_value=context), \
                patch.object(capacity_profile.socket, "create_connection", return_value=FakeRaw()):
            return capacity_profile.site_get("example.localhost")

    def test_http_failure_includes_bounded_response_detail(self):
        status, elapsed, detail, timings = self.probe(
            b"HTTP/1.1 503 Service Unavailable\r\nContent-Type: text/plain\r\n\r\nphp-fpm saturated"
        )
        self.assertEqual(status, 503)
        self.assertGreaterEqual(elapsed, 0)
        self.assertIn("php-fpm saturated", detail)
        self.assertGreaterEqual(timings["tcp_connect_s"], 0)
        self.assertGreaterEqual(timings["tls_handshake_s"], 0)
        self.assertGreaterEqual(timings["time_to_first_byte_s"], 0)
        self.assertGreaterEqual(timings["response_transfer_s"], 0)
        self.assertEqual(timings["failed_phase"], "")

    def test_connection_failure_is_reported(self):
        with patch.object(capacity_profile.ssl, "create_default_context", return_value=FakeContext(b"")), \
                patch.object(capacity_profile.socket, "create_connection", side_effect=TimeoutError("probe timeout")):
            status, _elapsed, detail, timings = capacity_profile.site_get("example.localhost")
        self.assertEqual(status, 0)
        self.assertIn("probe timeout", detail)
        self.assertEqual(timings["failed_phase"], "tcp_connect")
        self.assertIsNone(timings["tls_handshake_s"])

    def test_response_detail_is_bounded(self):
        response = b"HTTP/1.1 500 Error\r\nX-Test: yes\r\n\r\n" + b"x" * 4096
        _status, _elapsed, detail, _timings = self.probe(response)
        self.assertLessEqual(len(detail), 512)

    def test_server_response_timeout_is_attributed_to_first_byte_phase(self):
        class TimeoutTLS(FakeTLS):
            def recv(self, _size):
                raise TimeoutError("waiting for first response byte")

        context = FakeContext(b"")
        context.wrap_socket = lambda _raw, server_hostname: TimeoutTLS(b"")
        with patch.object(capacity_profile.ssl, "create_default_context", return_value=context), \
                patch.object(capacity_profile.socket, "create_connection", return_value=FakeRaw()):
            status, _elapsed, detail, timings = capacity_profile.site_get("example.localhost")
        self.assertEqual(status, 0)
        self.assertIn("first response byte", detail)
        self.assertEqual(timings["failed_phase"], "server_response")
        self.assertGreaterEqual(timings["time_to_first_byte_s"], 0)

    def test_tls_handshake_failure_is_attributed_to_tls_phase(self):
        context = FakeContext(b"")
        context.wrap_socket = lambda _raw, server_hostname: (_ for _ in ()).throw(
            TimeoutError("TLS handshake timeout"))
        with patch.object(capacity_profile.ssl, "create_default_context", return_value=context), \
                patch.object(capacity_profile.socket, "create_connection", return_value=FakeRaw()):
            status, _elapsed, detail, timings = capacity_profile.site_get("example.localhost")
        self.assertEqual(status, 0)
        self.assertIn("TLS handshake timeout", detail)
        self.assertEqual(timings["failed_phase"], "tls_handshake")
        self.assertGreaterEqual(timings["tls_handshake_s"], 0)

    def test_site_runtime_metrics_are_aggregated_with_sample_age(self):
        body = "\n".join([
            'stepanel_site_fpm_probe_up{site="cap-001"} 1',
            'stepanel_site_fpm_saturated{site="cap-001",php_version="8.4"} 1',
            'stepanel_site_fpm_active_workers{site="cap-001",php_version="8.4"} 5',
            'stepanel_site_fpm_active_workers{site="cap-001",php_version="8.3"} 8',
            'stepanel_site_fpm_listen_queue{site="cap-001",php_version="8.4"} 3',
            'stepanel_site_fpm_listen_queue{site="cap-001",php_version="8.3"} 0',
            'stepanel_site_fpm_slow_requests{site="cap-001",php_version="8.4"} 2',
            'stepanel_site_fpm_slow_requests{site="cap-001",php_version="8.3"} 11',
            'stepanel_site_fpm_sample_timestamp_seconds{site="cap-001"} 900',
            'stepanel_site_resource_cpu_quota_utilization_percent{site="cap-001"} 87.5',
            'stepanel_site_resource_memory_current_bytes{site="cap-001"} 75',
            'stepanel_site_resource_memory_max_bytes{site="cap-001"} 100',
            'stepanel_site_resource_memory_current_bytes{site="cap-002"} 0',
            'stepanel_site_resource_memory_max_bytes{site="cap-002"} 0',
            'stepanel_site_fpm_active_workers{site="not a site"} 99',
            'unrelated_metric{site="cap-001"} 99',
        ])
        sample = capacity_profile.parse_site_runtime_metrics(body, now=1000)
        self.assertEqual(sample["sites_sampled"], 2)
        self.assertEqual(sample["fpm_available_sites"], 1)
        self.assertEqual(sample["saturated_sites"], 1)
        self.assertEqual(sample["max_active_workers"], 8)
        self.assertEqual(sample["max_listen_queue"], 3)
        self.assertEqual(sample["max_slow_requests"], 11)
        self.assertEqual(sample["max_site_cpu_quota_utilization_pct"], 87.5)
        self.assertEqual(sample["max_site_memory_limit_utilization_pct"], 75.0)
        self.assertEqual(sample["oldest_fpm_sample_age_s"], 100.0)
        self.assertEqual(sample["fpm_unavailable_site_count"], 1)
        self.assertEqual(sample["fpm_unavailable_sites"], ["cap-002"])
        self.assertEqual(sample["saturated_site_names"], ["cap-001"])

    def test_site_runtime_metric_summary_marks_failed_scrapes(self):
        summary = capacity_profile.summarize_runtime_metrics([
            {"status": 200, "metrics": {"saturated_sites": 2, "max_listen_queue": 4,
                                         "fpm_unavailable_site_count": 3,
                                         "fpm_unavailable_sites": ["cap-1", "cap-2"],
                                         "saturated_site_names": ["cap-4"]}},
            {"status": 503, "detail": "metrics unavailable"},
        ])
        self.assertEqual(summary["scrapes"], 2)
        self.assertEqual(summary["failed_scrapes"], 1)
        self.assertEqual(summary["peak"]["saturated_sites"], 2)
        self.assertEqual(summary["peak"]["max_listen_queue"], 4)
        self.assertEqual(summary["max_sites_without_fpm_sample"], 3)
        self.assertEqual(summary["fpm_unavailable_site_samples"], ["cap-1", "cap-2"])
        self.assertEqual(summary["saturated_site_samples"], ["cap-4"])

    def test_window_summary_accepts_site_and_admin_sample_shapes(self):
        site_results = [
            (10, 200, 0.1, {"total_s": 0.1}),
            (20, 503, 0.2, {"total_s": 0.2}),
            (30, 200, 0.3, {"total_s": 0.3}),
        ]
        admin_results = [
            (10, 200, 0.01),
            (20, 503, 0.02),
            (30, 200, 0.03),
        ]

        self.assertEqual(
            capacity_profile.window_latencies(site_results, False, 20, 30),
            [0.1],
        )
        self.assertEqual(
            capacity_profile.window_latencies(admin_results, True, 20, 30),
            [0.03],
        )

    def test_window_status_counts_include_failures_for_each_window(self):
        results = [
            (10, 200, 0.1, {}),
            (15, 0, 15.0, {}),
            (20, 503, 0.2, {}),
            (30, 200, 0.3, {}),
        ]
        baseline = capacity_profile.window_status_counts(results, False, 20, 25)
        heavy = capacity_profile.window_status_counts(results, True, 20, 25)
        self.assertEqual(baseline["requests"], 3)
        self.assertEqual(baseline["errors"], 1)
        self.assertEqual(baseline["status_counts"], {0: 1, 200: 2})
        self.assertEqual(heavy["requests"], 1)
        self.assertEqual(heavy["errors"], 1)
        self.assertEqual(heavy["status_counts"], {503: 1})

    def test_failed_site_runtime_diagnostic_is_site_scoped_and_reports_missing_data(self):
        body = "\n".join([
            'stepanel_site_fpm_probe_up{site="cap-001"} 1',
            'stepanel_site_fpm_saturated{site="cap-001",php_version="8.4"} 1',
            'stepanel_site_fpm_active_workers{site="cap-001",php_version="8.4"} 3',
            'stepanel_site_fpm_active_workers{site="cap-001",php_version="8.3"} 2',
            'stepanel_site_fpm_listen_queue{site="cap-001",php_version="8.4"} 4',
            'stepanel_site_fpm_slow_requests{site="cap-001",php_version="8.4"} 7',
            'stepanel_site_fpm_sample_timestamp_seconds{site="cap-001"} 900',
            'stepanel_site_resource_cpu_quota_utilization_percent{site="cap-001"} 80',
            'stepanel_site_resource_memory_current_bytes{site="cap-001"} 75',
            'stepanel_site_resource_memory_max_bytes{site="cap-001"} 100',
            'stepanel_site_fpm_probe_up{site="cap-002"} 0',
        ])
        details = capacity_profile.parse_site_runtime_diagnostic(
            body, "cap-001", now=1000)
        self.assertEqual(details["fpm_probe_up"], 1)
        self.assertTrue(details["fpm_saturated"])
        self.assertEqual(details["fpm_active_workers"], 5)
        self.assertEqual(details["fpm_listen_queue"], 4)
        self.assertEqual(details["fpm_slow_requests"], 7)
        self.assertEqual(details["cpu_quota_utilization_pct"], 80)
        self.assertEqual(details["memory_limit_utilization_pct"], 75)
        self.assertEqual(details["fpm_sample_age_s"], 100)
        self.assertEqual(
            capacity_profile.parse_site_runtime_diagnostic(body, "cap-003"),
            {"site": "cap-003", "sampled": False},
        )


if __name__ == "__main__":
    unittest.main()


class RequestTimelineTest(unittest.TestCase):
    def test_buckets_by_request_start_time(self):
        started = 1000.0
        results = [
            (1001.0, 200, 0.5, {}),   # started at 0.5 s
            (1018.0, 0, 15.0, {}),    # started at 3 s, timed out
            (1007.0, 200, 0.1, {}),   # started at 6.9 s
        ]
        self.assertEqual(capacity_profile.request_timeline(results, started), [[0, 1, 1], [5, 1, 0]])


class EnvelopeSeedTests(unittest.TestCase):
    def test_alias_domains_reach_the_envelope_ratio(self):
        sites = [f"cap-{index:03d}" for index in range(500)]
        aliases = capacity_profile.alias_domains(sites, 2000)
        self.assertEqual(len(aliases), 1500)
        self.assertEqual(len({domain for _, domain in aliases}), 1500)
        per_site = {}
        for site, domain in aliases:
            self.assertTrue(domain.endswith(f"-{site}.localhost"), domain)
            per_site[site] = per_site.get(site, 0) + 1
        self.assertEqual(set(per_site.values()), {3})

    def test_alias_domains_never_go_negative(self):
        self.assertEqual(capacity_profile.alias_domains(["cap-000", "cap-001"], 1), [])
        self.assertEqual(capacity_profile.alias_domains([], 10), [])

    def test_accounts_own_disjoint_sites_within_the_plan_limit(self):
        sites = [f"cap-{index:03d}" for index in range(500)]
        plan = capacity_profile.plan_accounts(sites, 100, 5)
        self.assertEqual(len(plan), 100)
        owned = [site for assigned in plan.values() for site in assigned]
        self.assertEqual(sorted(owned), sites)
        self.assertTrue(all(len(assigned) <= 5 for assigned in plan.values()))
        self.assertTrue(all(len(name) <= 32 and name.isalnum() for name in plan))

    def test_sites_beyond_the_account_capacity_stay_unassigned(self):
        plan = capacity_profile.plan_accounts([f"s{index}" for index in range(11)], 2, 5)
        self.assertEqual(plan, {"capacct000": ["s0", "s1", "s2", "s3", "s4"], "capacct001": ["s5", "s6", "s7", "s8", "s9"]})
        self.assertEqual(capacity_profile.plan_accounts(["s0"], 0, 5), {})

    def test_isolation_requires_exactly_the_owned_sites(self):
        self.assertIsNone(capacity_profile.isolation_violation(["a", "b"], ["b", "a"]))
        self.assertIn("unexpected ['c']", capacity_profile.isolation_violation(["a", "b"], ["a", "b", "c"]))
        self.assertIn("missing ['b']", capacity_profile.isolation_violation(["a", "b"], ["a"]))


class ScheduledAndRestartTests(unittest.TestCase):
    def test_parse_job_time_handles_nanoseconds_and_z(self):
        self.assertAlmostEqual(capacity_profile.parse_job_time("2026-11-25T02:40:17.123456789Z"),
                               capacity_profile.parse_job_time("2026-11-25T02:40:17.123456Z"), places=5)
        self.assertIsNone(capacity_profile.parse_job_time(""))
        # Go trims trailing zeros; Python 3.9 accepts only 3 or 6 digits.
        self.assertAlmostEqual(capacity_profile.parse_job_time("2026-11-25T02:40:17.5Z") % 60, 17.5, places=5)

    def test_errors_inside_a_restart_window_are_excluded(self):
        results = [(10.0, 200, 0.1), (20.0, 503, 0.1), (21.0, 0, 0.1), (40.0, 500, 0.1)]
        self.assertEqual(capacity_profile.errors_outside_windows(results, [(19.0, 25.0)]), 1)
        self.assertEqual(capacity_profile.errors_outside_windows(results, []), 3)

    def test_scheduled_jobs_must_run_complete_and_not_duplicate(self):
        base = capacity_profile.parse_job_time("2026-11-25T03:00:00Z")
        def job(site, minute, state="completed", kind="site.backup", job_id=None):
            return {"kind": kind, "user": site, "state": state, "id": job_id or f"{site}-{minute}",
                    "started_at": f"2026-11-25T03:{minute:02d}:00Z"}
        jobs = [job("cap-000", 5), job("cap-000", 10), job("cap-001", 5), job("cap-002", 1, kind="site.create")]
        count, problems = capacity_profile.scheduled_job_findings(jobs, ["cap-000", "cap-001"], base, 150)
        self.assertEqual((count, problems), (3, []))
        count, problems = capacity_profile.scheduled_job_findings(
            jobs + [job("cap-001", 6, job_id="dup")], ["cap-000", "cap-001", "cap-002"], base, 150)
        self.assertEqual(count, 4)
        self.assertTrue(any("dup ran 60s" in p for p in problems), problems)
        self.assertTrue(any("cap-002: no scheduled backup ran" in p for p in problems), problems)
        _, problems = capacity_profile.scheduled_job_findings([job("cap-000", 5, state="dead-letter")], ["cap-000"], base, 150)
        self.assertTrue(any("ended dead-letter" in p for p in problems), problems)
        # Jobs that started before measurement are not counted.
        count, problems = capacity_profile.scheduled_job_findings([job("cap-000", 5)], ["cap-000"], base + 3600, 150)
        self.assertEqual(count, 0)
