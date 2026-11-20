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


if __name__ == "__main__":
    unittest.main()
