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
        status, elapsed, detail = self.probe(
            b"HTTP/1.1 503 Service Unavailable\r\nContent-Type: text/plain\r\n\r\nphp-fpm saturated"
        )
        self.assertEqual(status, 503)
        self.assertGreaterEqual(elapsed, 0)
        self.assertIn("php-fpm saturated", detail)

    def test_connection_failure_is_reported(self):
        with patch.object(capacity_profile.ssl, "create_default_context", return_value=FakeContext(b"")), \
                patch.object(capacity_profile.socket, "create_connection", side_effect=TimeoutError("probe timeout")):
            status, _elapsed, detail = capacity_profile.site_get("example.localhost")
        self.assertEqual(status, 0)
        self.assertIn("probe timeout", detail)

    def test_response_detail_is_bounded(self):
        response = b"HTTP/1.1 500 Error\r\nX-Test: yes\r\n\r\n" + b"x" * 4096
        _status, _elapsed, detail = self.probe(response)
        self.assertLessEqual(len(detail), 512)


if __name__ == "__main__":
    unittest.main()
