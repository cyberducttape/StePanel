#!/usr/bin/env python3
"""Installed-host capacity profile for StePanel.

Runs on a disposable host where StePanel is installed (the lab install smoke
leaves the panel at 127.0.0.1:8090 with known lab credentials). It:

  1. creates N live PHP sites through the API, each with a route served by
     the real web server and PHP-FPM, then adds alias domain routes until the
     host carries CAPACITY_DOMAINS domains (four per site by default, the
     envelope's 2,000 for 500 sites), checks that every domain answers, and
     creates CAPACITY_ACCOUNTS customer accounts (one per five sites by
     default, the envelope's 100 for 500 sites) that own those sites;
  2. for CAPACITY_DURATION seconds, sends customer traffic to every domain,
     continuously probes the administrative API, and runs logged-in customer
     probes that time tenant-scoped API calls and check that each customer
     sees exactly its own sites;
  3. a quarter of the way in, starts two heavy customer operations at once
     (backups of large incompressible sites) and measures their queue wait
     and duration; meanwhile CAPACITY_SCHEDULED_SITES sites carry backup
     schedules every CAPACITY_SCHEDULE_MINUTES (five sites every five
     minutes by default, 60 scheduled jobs an hour against the envelope's
     50), and every scheduled job must complete exactly once;
  3b. three quarters of the way in (CAPACITY_RESTART=1, the default),
     restarts the panel and worker under load, measures the time until the
     host is ready again, and requires site traffic to keep succeeding and
     admin and customer probes to succeed outside the restart window;
  4. samples CPU, memory, disk I/O, process RSS, and control-plane
     database-lock errors throughout;

and prints a Markdown report (also written as JSON next to it). Profiles:
small (25 sites), node (150), large (500); CAPACITY_SITES overrides.

This produces measurements for one host and run, not a certified limit.
Lab only: it creates and leaves sites named cap-* and cap-heavy-*, alias
domains alias*-cap-*.localhost, and customer accounts named capacct*.
"""

import base64
import glob
import hashlib
import hmac
import http.cookiejar
import json
import os
import random
import re
import socket
import ssl
import struct
import subprocess
import sys
import threading
import time
import platform
import urllib.error
import urllib.parse
import urllib.request
from collections import Counter

PANEL = os.environ.get("PANEL", "http://127.0.0.1:8090")
USER = os.environ.get("STEPANEL_ADMIN_USERNAME", "admin")
PASSWORD = os.environ.get("STEPANEL_ADMIN_PASSWORD", "ci-install-only-password")
TOTP_SECRET = os.environ.get("STEPANEL_ADMIN_TOTP_SECRET", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
PROFILES = {"small": 25, "node": 150, "large": 500}
PROFILE = os.environ.get("CAPACITY_PROFILE", "small")
SITES = int(os.environ.get("CAPACITY_SITES", PROFILES.get(PROFILE, 25)))
DURATION = int(os.environ.get("CAPACITY_DURATION", "600"))
CLIENTS = int(os.environ.get("CAPACITY_SITE_CLIENTS", "8"))
HEAVY_MIB = int(os.environ.get("CAPACITY_HEAVY_MIB", "512"))
REPORT = os.environ.get("CAPACITY_REPORT", "/var/log/stepanel-cert/capacity-report.json")
DOMAINS = int(os.environ.get("CAPACITY_DOMAINS", 4 * SITES))
# The professional plan allows five sites per account.
ACCOUNT_SITE_LIMIT = 5
ACCOUNTS = int(os.environ.get("CAPACITY_ACCOUNTS", SITES // ACCOUNT_SITE_LIMIT))
CUSTOMER_PROBES = int(os.environ.get("CAPACITY_CUSTOMER_PROBES", "4"))
SCHEDULED_SITES = int(os.environ.get("CAPACITY_SCHEDULED_SITES", "5"))
SCHEDULE_MINUTES = int(os.environ.get("CAPACITY_SCHEDULE_MINUTES", "5"))
RESTART = os.environ.get("CAPACITY_RESTART", "1") == "1"
RESTART_AT = float(os.environ.get("CAPACITY_RESTART_AT", "0.75"))
# Probes are retried while the panel restarts; failures within this many
# seconds after readiness returns are part of the restart window.
RESTART_SETTLE_S = 5


def parse_job_time(value):
    """Parse the API's RFC 3339 timestamps (Z suffix, nanoseconds) to epoch
    seconds without third-party modules."""
    if not value:
        return None
    text = value.replace("Z", "+00:00")
    main, _, zone = text.partition("+")
    if "." in main:
        whole, fraction = main.split(".", 1)
        main = whole + "." + fraction[:6].ljust(6, "0")
    from datetime import datetime
    return datetime.fromisoformat(main + ("+" + zone if zone else "")).timestamp()


def errors_outside_windows(results, windows):
    """Count failed probes (status outside 200-399) not inside any window."""
    def inside(t):
        return any(begin <= t <= end for begin, end in windows)
    return sum(1 for (t, status, _) in results if not 200 <= status < 400 and not inside(t))


def scheduled_job_findings(jobs, sites, since, min_gap_s):
    """Check scheduled backup jobs for the given sites that started at or
    after since: every site ran, every job completed, and no site ran twice
    within min_gap_s (a duplicated schedule slot). Returns (count, problems)."""
    by_site = {site: [] for site in sites}
    for job in jobs:
        if job.get("kind") != "site.backup" or job.get("user") not in by_site:
            continue
        started = parse_job_time(job.get("started_at"))
        if started is None or started < since:
            continue
        by_site[job["user"]].append((started, job.get("state"), job.get("id")))
    problems = []
    count = 0
    for site, runs in sorted(by_site.items()):
        runs.sort()
        count += len(runs)
        if not runs:
            problems.append(f"{site}: no scheduled backup ran")
        for started, state, job_id in runs:
            if state != "completed":
                problems.append(f"{site}: scheduled backup {job_id} ended {state}")
        for (first, _, _), (second, _, second_id) in zip(runs, runs[1:]):
            if second - first < min_gap_s:
                problems.append(f"{site}: scheduled backup {second_id} ran {round(second - first)}s after the previous one")
    return count, problems


def alias_domains(site_names, total_domains):
    """Alias domains to add so the host carries total_domains routes: one
    primary per site already exists; the rest are spread round-robin."""
    extra = max(total_domains - len(site_names), 0)
    if not site_names:
        return []
    return [(site_names[i % len(site_names)], f"alias{i // len(site_names) + 1}-{site_names[i % len(site_names)]}.localhost")
            for i in range(extra)]


def plan_accounts(site_names, accounts, per_account):
    """Assign consecutive sites to accounts named capacct000...; every site
    goes to at most one account, no account exceeds its plan limit, and sites
    beyond accounts * per_account stay with the administrator."""
    if accounts <= 0 or per_account <= 0:
        return {}
    plan = {}
    for index in range(accounts):
        owned = site_names[index * per_account:(index + 1) * per_account]
        plan[f"capacct{index:03d}"] = owned
    return plan


def isolation_violation(expected, listed):
    """A tenant-scoped listing must show exactly the sites the customer owns.
    Returns a description of the difference, or None."""
    expected, listed = set(expected), set(listed)
    if listed == expected:
        return None
    return f"unexpected {sorted(listed - expected)[:5]}, missing {sorted(expected - listed)[:5]}"


def log(message):
    print(f"[capacity {time.strftime('%H:%M:%S')}] {message}", file=sys.stderr, flush=True)


def totp(secret):
    secret = secret.strip().upper()
    secret += "=" * ((8 - len(secret) % 8) % 8)
    key = base64.b32decode(secret)
    digest = hmac.new(key, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    return f"{(struct.unpack('>I', digest[offset:offset + 4])[0] & 0x7FFFFFFF) % 1000000:06d}"


class Panel:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ""

    def login(self, user=USER, password=PASSWORD, secret=TOTP_SECRET, fresh_window=True):
        # A fresh TOTP window, so an earlier lab login's code is not replayed.
        if fresh_window:
            time.sleep(31 - int(time.time()) % 30)
        self.opener.open(PANEL + "/login", timeout=10).read()
        body = urllib.parse.urlencode({"username": user, "password": password, "totp": totp(secret)}).encode()
        self.opener.open(urllib.request.Request(PANEL + "/login", data=body), timeout=10).read()
        for cookie in self.jar:
            if cookie.name == "stepanel_csrf":
                self.csrf = cookie.value
        if not self.csrf:
            raise SystemExit("login did not issue a CSRF cookie")

    def call(self, method, path, payload=None, timeout=120):
        data = json.dumps(payload).encode() if payload is not None else None
        request = urllib.request.Request(PANEL + path, data=data, method=method)
        request.add_header("X-CSRF-Token", self.csrf)
        if data is not None:
            request.add_header("Content-Type", "application/json")
        try:
            with self.opener.open(request, timeout=timeout) as response:
                body = response.read()
                return response.status, (json.loads(body) if body.strip().startswith(b"{") else body)
        except urllib.error.HTTPError as error:
            return error.code, error.read()

    def wait_job(self, job_id, limit=3600, poll=1.0):
        deadline = time.time() + limit
        while time.time() < deadline:
            status, job = self.call("GET", "/api/jobs/" + job_id)
            if status == 200 and job.get("state") in ("completed", "failed", "dead-letter", "cancelled"):
                return job
            time.sleep(poll)
        raise SystemExit(f"job {job_id} did not finish in {limit}s")


def failure_detail(body):
    """The failing readiness checks, or the start of an error body."""
    if isinstance(body, dict):
        checks = body.get("checks")
        if isinstance(checks, dict):
            failing = [f"{name}: {check.get('detail', '')}" for name, check in checks.items()
                       if isinstance(check, dict) and not check.get("ready")]
            if failing:
                return "; ".join(failing)
        return json.dumps(body)[:300]
    if isinstance(body, bytes):
        try:
            return failure_detail(json.loads(body))
        except ValueError:
            return body[:300].decode(errors="replace")
    return str(body)[:300]


def site_get(domain, path="/"):
    """GET a path and separate connection, TLS, first-byte, and body timings."""
    started = time.perf_counter()
    phase_started = started
    phase = "client_setup"
    timings = {"tcp_connect_s": None, "tls_handshake_s": None,
               "time_to_first_byte_s": None, "response_transfer_s": None,
               "client_setup_s": None}
    captured = bytearray()
    try:
        context = ssl.create_default_context()
        context.check_hostname = False
        context.verify_mode = ssl.CERT_NONE
        phase_started = time.perf_counter()
        timings["client_setup_s"] = phase_started - started
        phase = "tcp_connect"
        with socket.create_connection(("127.0.0.1", 443), timeout=15) as raw:
            connected = time.perf_counter()
            timings["tcp_connect_s"] = connected - phase_started
            phase = "tls_handshake"
            phase_started = time.perf_counter()
            with context.wrap_socket(raw, server_hostname=domain) as tls:
                request_started = time.perf_counter()
                timings["tls_handshake_s"] = request_started - phase_started
                phase = "server_response"
                phase_started = request_started
                tls.sendall(f"GET {path} HTTP/1.1\r\nHost: {domain}\r\nConnection: close\r\n\r\n".encode())
                while len(captured) < 4096 and b"\r\n" not in captured:
                    chunk = tls.recv(4096 - len(captured))
                    if not chunk:
                        break
                    if timings["time_to_first_byte_s"] is None:
                        first_byte = time.perf_counter()
                        timings["time_to_first_byte_s"] = first_byte - phase_started
                        phase = "response_transfer"
                        phase_started = first_byte
                    captured.extend(chunk)
                while len(captured) < 1024:
                    chunk = tls.recv(min(65536, 1024 - len(captured)))
                    if not chunk:
                        break
                    if timings["time_to_first_byte_s"] is None:
                        first_byte = time.perf_counter()
                        timings["time_to_first_byte_s"] = first_byte - request_started
                        phase = "response_transfer"
                        phase_started = first_byte
                    captured.extend(chunk)
                for _ in iter(lambda: tls.recv(65536), b""):
                    pass
                if timings["time_to_first_byte_s"] is None:
                    timings["time_to_first_byte_s"] = time.perf_counter() - request_started
                timings["response_transfer_s"] = time.perf_counter() - phase_started
        first_line = bytes(captured).split(b"\r\n", 1)[0]
        status = int(first_line.split(b" ", 2)[1])
        detail = bytes(captured).decode(errors="replace")[:512]
    except Exception as error:
        status = 0
        if phase == "client_setup":
            timings["client_setup_s"] = time.perf_counter() - phase_started
        elif phase == "tcp_connect":
            timings["tcp_connect_s"] = time.perf_counter() - phase_started
        elif phase == "tls_handshake":
            timings["tls_handshake_s"] = time.perf_counter() - phase_started
        elif phase == "server_response":
            timings["time_to_first_byte_s"] = time.perf_counter() - phase_started
        elif phase == "response_transfer":
            timings["response_transfer_s"] = time.perf_counter() - phase_started
        detail = f"{type(error).__name__}: {error}; response={bytes(captured).decode(errors='replace')[:400]}"
    timings["total_s"] = time.perf_counter() - started
    return status, timings["total_s"], detail, {**timings, "failed_phase": phase if status == 0 else ""}


def percentile(values, p):
    if not values:
        return None
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(round(p / 100 * (len(ordered) - 1))))]


def summarize(latencies):
    return {
        "count": len(latencies),
        "p50_ms": round(percentile(latencies, 50) * 1000, 1) if latencies else None,
        "p95_ms": round(percentile(latencies, 95) * 1000, 1) if latencies else None,
        "p99_ms": round(percentile(latencies, 99) * 1000, 1) if latencies else None,
        "max_ms": round(max(latencies) * 1000, 1) if latencies else None,
    }


def window_latencies(results, inside, begin, end):
    """Return successful sample latencies in or outside an operation window.

    Site samples have a fourth timing-details field while administrative
    samples do not. The common timestamp/status/latency prefix is intentional.
    """
    return [row[2] for row in results
            if 200 <= row[1] < 400 and ((begin <= row[0] <= end) == inside)]


def window_status_counts(results, inside, begin, end):
    """Count successful and failed requests in one measurement window."""
    selected = [row for row in results
                if ((begin <= row[0] <= end) == inside)]
    return {
        "requests": len(selected),
        "errors": sum(not 200 <= row[1] < 400 for row in selected),
        "status_counts": dict(sorted(Counter(row[1] for row in selected).items())),
    }


def request_timeline(results, started, bucket_s=5):
    """Count successes and failures per bucket of request start time.

    Each row is [bucket start s, successes, failures]. A host-wide stall
    shows as buckets where every client's requests fail together; a per-site
    fault shows as failures spread across otherwise healthy buckets.
    """
    buckets = {}
    for finished, status, seconds, _ in results:
        start = max(0.0, finished - seconds - started)
        row = buckets.setdefault(int(start // bucket_s) * bucket_s, [0, 0])
        row[0 if 200 <= status < 400 else 1] += 1
    return [[key, ok, failed] for key, (ok, failed) in sorted(buckets.items())]


def parse_site_runtime_diagnostic(body, site_name, now=None):
    """Return bounded FPM and resource evidence for one site from /metrics."""
    if isinstance(body, bytes):
        body = body.decode(errors="replace")
    now = time.time() if now is None else now
    values = {}
    for line in body.splitlines():
        match = SITE_METRIC.match(line)
        if not match:
            continue
        metric, labels, raw_value = match.groups()
        site = SITE_LABEL.search(labels)
        if not site or site.group(1) != site_name:
            continue
        try:
            value = float(raw_value)
        except ValueError:
            continue
        values.setdefault(metric, []).append(value)
    if not values:
        return {"site": site_name, "sampled": False}

    timestamps = values.get("stepanel_site_fpm_sample_timestamp_seconds", [])
    memory = list(zip(values.get("stepanel_site_resource_memory_current_bytes", []),
                      values.get("stepanel_site_resource_memory_max_bytes", [])))
    memory_pct = [100 * current / limit for current, limit in memory if limit > 0]
    return {
        "site": site_name,
        "sampled": True,
        "fpm_probe_up": max(values.get("stepanel_site_fpm_probe_up", []), default=None),
        "fpm_saturated": any(v != 0 for v in values.get("stepanel_site_fpm_saturated", [])),
        "fpm_active_workers": sum(values.get("stepanel_site_fpm_active_workers", [])),
        "fpm_listen_queue": sum(values.get("stepanel_site_fpm_listen_queue", [])),
        "fpm_slow_requests": sum(values.get("stepanel_site_fpm_slow_requests", [])),
        "cpu_quota_utilization_pct": max(
            values.get("stepanel_site_resource_cpu_quota_utilization_percent", []), default=None),
        "memory_limit_utilization_pct": round(max(memory_pct), 1) if memory_pct else None,
        "fpm_sample_age_s": round(max(0, now - max(timestamps)), 1) if timestamps else None,
    }


def add_route(panel, name, domain):
    status, preview = panel.call("POST", "/api/sites/deploy", {"site": name, "domain": domain})
    if status != 200:
        raise SystemExit(f"route preview {name} {domain}: HTTP {status} {preview!r}")
    status, applied = panel.call("POST", "/api/sites/deploy", {"site": name, "domain": domain, "preview_token": preview["preview_token"]})
    if status != 202:
        raise SystemExit(f"route apply {name} {domain}: HTTP {status} {applied!r}")


def create_site(panel, name, domain):
    status, created = panel.call("POST", "/api/sites", {"site": name, "template": "php"})
    if status != 202:
        raise SystemExit(f"create {name}: HTTP {status} {created!r}")
    job = panel.wait_job(created["job_id"])
    if job.get("state") != "completed":
        raise SystemExit(f"create {name}: {job.get('state')} {job.get('error')}")
    status, preview = panel.call("POST", "/api/sites/deploy", {"site": name, "domain": domain})
    if status != 200:
        raise SystemExit(f"route preview {name}: HTTP {status} {preview!r}")
    status, applied = panel.call("POST", "/api/sites/deploy", {"site": name, "domain": domain, "preview_token": preview["preview_token"]})
    if status != 202:
        raise SystemExit(f"route apply {name}: HTTP {status} {applied!r}")
    return created.get("document_root")


def proc_cpu():
    with open("/proc/stat") as stat:
        fields = [int(v) for v in stat.readline().split()[1:]]
    idle = fields[3] + fields[4]
    return sum(fields), idle


def rss_mib(match):
    total = 0
    for pid in os.listdir("/proc"):
        if not pid.isdigit():
            continue
        try:
            with open(f"/proc/{pid}/cmdline", "rb") as handle:
                command = handle.read().replace(b"\0", b" ").decode(errors="replace")
            if match not in command:
                continue
            with open(f"/proc/{pid}/status") as handle:
                for line in handle:
                    if line.startswith("VmRSS:"):
                        total += int(line.split()[1])
        except OSError:
            continue
    return round(total / 1024, 1)


def fpm_master_stats():
    """Return (open fds, soft fd limit, live children) summed over PHP-FPM masters.

    One master serves every pool on the host, holding a listening socket per
    pool and log pipes per child, so its descriptor headroom bounds how many
    children it can start."""
    fds = limit = children = 0
    for pid in os.listdir("/proc"):
        if not pid.isdigit():
            continue
        try:
            with open(f"/proc/{pid}/cmdline", "rb") as handle:
                command = handle.read().replace(b"\0", b" ").decode(errors="replace")
            if command.startswith("php-fpm: pool "):
                children += 1
                continue
            if not command.startswith("php-fpm: master process"):
                continue
            fds += len(os.listdir(f"/proc/{pid}/fd"))
            with open(f"/proc/{pid}/limits") as handle:
                for line in handle:
                    if line.startswith("Max open files"):
                        limit += int(line.split()[3])
        except (OSError, ValueError, IndexError):
            continue
    return fds, limit, children


FPM_LOG_PATTERNS = ("Too many open files", "fork() failed", "process.max", "max_children")


def fpm_log_offsets():
    offsets = {}
    for path in glob.glob("/var/log/php-fpm/*.log") + glob.glob("/var/log/php*-fpm.log"):
        try:
            offsets[path] = os.path.getsize(path)
        except OSError:
            continue
    return offsets


def fpm_log_errors(offsets, since):
    """Count PHP-FPM master warnings written since the measurement began."""
    counts = {pattern: 0 for pattern in FPM_LOG_PATTERNS}
    examples = []
    texts = []
    for path in glob.glob("/var/log/php-fpm/*.log") + glob.glob("/var/log/php*-fpm.log"):
        try:
            with open(path, errors="replace") as handle:
                handle.seek(offsets.get(path, 0))
                texts.append(handle.read())
        except OSError:
            continue
    units = subprocess.run(
        ["systemctl", "list-units", "--type=service", "--no-legend", "--plain", "php*-fpm.service", "php-fpm.service"],
        capture_output=True, text=True).stdout.split()
    units = [unit for unit in units if unit.endswith(".service")]
    if units:
        args = ["journalctl", "--since", since, "--no-pager", "-q"]
        for unit in units:
            args += ["-u", unit]
        texts.append(subprocess.run(args, capture_output=True, text=True).stdout)
    for text in texts:
        for line in text.splitlines():
            matched = [pattern for pattern in FPM_LOG_PATTERNS if pattern in line]
            for pattern in matched:
                counts[pattern] += 1
            if matched and len(examples) < 10:
                examples.append(line[-300:])
    return {"counts": counts, "examples": examples}


WHOLE_DISK = re.compile(r"^(vd[a-z]+|sd[a-z]+|xvd[a-z]+|nvme[0-9]+n[0-9]+)$")
SITE_METRIC = re.compile(
    r'^(stepanel_site_(?:fpm_[a-z_]+|resource_[a-z_]+))\{([^}]*)\}\s+([0-9.eE+-]+)$'
)
SITE_LABEL = re.compile(r'(?:^|,)site="([a-z0-9_-]{1,32})"(?:,|$)')


def parse_site_runtime_metrics(body, now=None):
    """Summarize the current bounded per-site Prometheus snapshot."""
    if isinstance(body, bytes):
        body = body.decode(errors="replace")
    now = time.time() if now is None else now
    values = {}
    for line in body.splitlines():
        match = SITE_METRIC.match(line)
        if not match:
            continue
        metric, labels, raw_value = match.groups()
        site = SITE_LABEL.search(labels)
        if not site:
            continue
        try:
            value = float(raw_value)
        except ValueError:
            continue
        values.setdefault(site.group(1), {}).setdefault(metric, []).append(value)

    timestamps = [stamp
                  for site in values.values()
                  for stamp in site.get("stepanel_site_fpm_sample_timestamp_seconds", [])]
    memory_ratios = [100 * current / limit
                     for site in values.values()
                     for current, limit in zip(
                         site.get("stepanel_site_resource_memory_current_bytes", []),
                         site.get("stepanel_site_resource_memory_max_bytes", []))
                     if limit > 0]
    all_values = lambda metric: [value for site in values.values()
                                 for value in site.get(metric, [])]
    fpm_unavailable_sites = sorted(
        site for site, metrics in values.items()
        if max(metrics.get("stepanel_site_fpm_probe_up", [0])) != 1)
    saturated_site_names = sorted(
        site for site, metrics in values.items()
        if any(value == 1 for value in metrics.get("stepanel_site_fpm_saturated", [])))
    return {
        "sites_sampled": len(values),
        "fpm_available_sites": sum(
            max(site.get("stepanel_site_fpm_probe_up", [0])) == 1
            for site in values.values()),
        "saturated_sites": sum(
            any(value == 1 for value in site.get("stepanel_site_fpm_saturated", []))
            for site in values.values()),
        "max_active_workers": max(all_values("stepanel_site_fpm_active_workers"), default=0),
        "max_listen_queue": max(all_values("stepanel_site_fpm_listen_queue"), default=0),
        "max_slow_requests": max(all_values("stepanel_site_fpm_slow_requests"), default=0),
        "max_site_cpu_quota_utilization_pct": max(
            all_values("stepanel_site_resource_cpu_quota_utilization_percent"), default=0),
        "max_site_memory_limit_utilization_pct": round(max(memory_ratios, default=0), 1),
        "oldest_fpm_sample_age_s": round(max((max(0, now - stamp) for stamp in timestamps), default=0), 1),
        "fpm_unavailable_site_count": len(fpm_unavailable_sites),
        "fpm_unavailable_sites": fpm_unavailable_sites[:25],
        "saturated_site_names": saturated_site_names[:25],
    }


def summarize_runtime_metrics(samples):
    usable = [sample["metrics"] for sample in samples if sample.get("status") == 200 and sample.get("metrics")]
    numeric = ("sites_sampled", "fpm_available_sites", "saturated_sites", "max_active_workers",
               "max_listen_queue", "max_slow_requests", "max_site_cpu_quota_utilization_pct",
               "max_site_memory_limit_utilization_pct", "oldest_fpm_sample_age_s")
    return {
        "scrapes": len(samples),
        "failed_scrapes": sum(sample.get("status") != 200 for sample in samples),
        "peak": {key: max((entry.get(key, 0) for entry in usable), default=0) for key in numeric},
        "max_sites_without_fpm_sample": max(
            (entry.get("fpm_unavailable_site_count", 0) for entry in usable), default=0),
        "fpm_unavailable_site_samples": sorted({
            site for entry in usable for site in entry.get("fpm_unavailable_sites", [])
        })[:25],
        "saturated_site_samples": sorted({
            site for entry in usable for site in entry.get("saturated_site_names", [])
        })[:25],
    }


def disk_sectors():
    """Sectors read and written by whole disks (not partitions)."""
    read = written = 0
    with open("/proc/diskstats") as stats:
        for line in stats:
            parts = line.split()
            if len(parts) > 9 and WHOLE_DISK.match(parts[2]):
                read += int(parts[5])
                written += int(parts[9])
    return read, written


class Sampler(threading.Thread):
    def __init__(self, stop):
        super().__init__(daemon=True)
        self.stop, self.samples = stop, []

    def run(self):
        last_total, last_idle = proc_cpu()
        last_read, last_written = disk_sectors()
        last_time = time.time()
        while not self.stop.wait(2):
            total, idle = proc_cpu()
            read, written = disk_sectors()
            now = time.time()
            with open("/proc/meminfo") as meminfo:
                memory = {line.split(":")[0]: int(line.split()[1]) for line in meminfo}
            elapsed = max(now - last_time, 0.001)
            fpm_fds, fpm_fd_limit, fpm_children = fpm_master_stats()
            self.samples.append({
                "fpm_master_fds": fpm_fds,
                "fpm_master_fd_limit": fpm_fd_limit,
                "fpm_children": fpm_children,
                "t": now,
                "cpu_pct": round(100 * (1 - (idle - last_idle) / max(total - last_total, 1)), 1),
                "mem_available_mib": round(memory["MemAvailable"] / 1024),
                "panel_worker_rss_mib": rss_mib("/opt/stepanel/stepanel"),
                "php_fpm_rss_mib": rss_mib("php-fpm"),
                "mariadb_rss_mib": rss_mib("mariadbd"),
                "disk_read_mib_s": round((read - last_read) * 512 / 1048576 / elapsed, 1),
                "disk_write_mib_s": round((written - last_written) * 512 / 1048576 / elapsed, 1),
            })
            last_total, last_idle, last_read, last_written, last_time = total, idle, read, written, now


def main():
    started_at = time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime())
    since = time.strftime("%Y-%m-%d %H:%M:%S")
    panel = Panel()
    panel.login()
    log(f"profile {PROFILE}: {SITES} sites, {DOMAINS} domains, {ACCOUNTS} accounts, {DURATION}s measurement, {CLIENTS} site clients, {CUSTOMER_PROBES} customer probes, two {HEAVY_MIB} MiB heavy backups")

    setup_started = time.time()
    domains = []
    for index in range(SITES):
        name = f"cap-{index:03d}"
        domain = f"{name}.localhost"
        docroot = create_site(panel, name, domain)
        domains.append(domain)
        if index == 0:
            static_docroot = docroot
        if (index + 1) % 25 == 0:
            log(f"created {index + 1}/{SITES} sites")
    heavy = []
    for index in range(2):
        name = f"cap-heavy-{index}"
        docroot = create_site(panel, name, f"{name}.localhost")
        if not docroot:
            raise SystemExit("site creation did not report the document root")
        owner = os.stat(docroot)
        with open(os.path.join(docroot, "payload.bin"), "wb") as payload:
            for _ in range(HEAVY_MIB):
                payload.write(os.urandom(1 << 20))
        os.chown(os.path.join(docroot, "payload.bin"), owner.st_uid, owner.st_gid)
        heavy.append(name)
    site_names = [f"cap-{index:03d}" for index in range(SITES)]
    aliases = alias_domains(site_names, DOMAINS)
    for count, (name, domain) in enumerate(aliases, 1):
        add_route(panel, name, domain)
        domains.append(domain)
        if count % 100 == 0:
            log(f"added {count}/{len(aliases)} alias domains")
    # Every route must actually be served before it carries measured traffic.
    unserved = []
    for domain in domains:
        status, _, detail, _ = site_get(domain)
        if not 200 <= status < 400:
            unserved.append(f"{domain}: HTTP {status} {detail[:120]}")
    if unserved:
        raise SystemExit(f"{len(unserved)} of {len(domains)} domains are not served: {unserved[:5]}")
    log(f"all {len(domains)} domains answered")
    account_plan = plan_accounts(site_names, ACCOUNTS, ACCOUNT_SITE_LIMIT)
    account_credentials = {}
    for count, (username, owned) in enumerate(account_plan.items(), 1):
        password = base64.urlsafe_b64encode(os.urandom(24)).decode()
        secret = base64.b32encode(os.urandom(20)).decode().rstrip("=")
        status, created = panel.call("POST", "/api/accounts", {"username": username, "password": password,
                                                               "totp_secret": secret, "plan": "professional", "sites": owned})
        if status != 201:
            raise SystemExit(f"create account {username}: HTTP {status} {created!r}")
        account_credentials[username] = (password, secret)
        if count % 25 == 0:
            log(f"created {count}/{len(account_plan)} customer accounts")
    setup_seconds = round(time.time() - setup_started)
    log(f"setup finished in {setup_seconds}s: {len(domains)} domains, {len(account_plan)} accounts")

    # Start measuring from a ready host. Setup can leave a transient readiness
    # check running (for example a scheduled off-site target probe); any
    # readiness failure after this point still counts against the run.
    ready_deadline = time.time() + 180
    while True:
        try:
            ready_status, ready_body = panel.call("GET", "/readyz", timeout=30)
        except OSError as error:
            ready_status, ready_body = 0, str(error)
        if 200 <= ready_status < 400:
            break
        if time.time() >= ready_deadline:
            raise SystemExit(f"host not ready 180s after setup: HTTP {ready_status} {failure_detail(ready_body)}")
        time.sleep(2)

    scheduled_sites = site_names[:max(SCHEDULED_SITES, 0)]
    for name in scheduled_sites:
        status, schedule = panel.call("PUT", "/api/backup-schedules", {
            "site": name, "interval_minutes": SCHEDULE_MINUTES, "keep_last": 2})
        if status != 200:
            raise SystemExit(f"backup schedule {name}: HTTP {status} {schedule!r}")
    if scheduled_sites:
        log(f"{len(scheduled_sites)} sites back up every {SCHEDULE_MINUTES} min ({len(scheduled_sites) * 60 // SCHEDULE_MINUTES} scheduled jobs/hour)")

    stop = threading.Event()
    fpm_offsets = fpm_log_offsets()
    fpm_since = time.strftime("%Y-%m-%d %H:%M:%S")
    sampler = Sampler(stop)
    sampler.start()
    lock = threading.Lock()
    site_results, site_failures, admin_results, admin_failures = [], [], {}, []
    runtime_metric_samples = []
    customer_results, customer_failures, isolation_failures = {}, [], []
    latest_runtime_metrics = {"sampled_at": None, "body": None}
    measurement_started = time.time()
    heavy_window = [None, None]

    def site_client():
        while not stop.is_set():
            domain = random.choice(domains)
            status, seconds, detail, timings = site_get(domain)
            runtime_evidence = None
            if not 200 <= status < 400:
                with lock:
                    metrics_body = latest_runtime_metrics["body"]
                    metrics_sampled_at = latest_runtime_metrics["sampled_at"]
                if metrics_body is not None:
                    runtime_evidence = parse_site_runtime_diagnostic(
                        metrics_body, domain.split(".", 1)[0], now=time.time())
                    runtime_evidence["scrape_age_s"] = round(
                        time.time() - metrics_sampled_at, 1)
            with lock:
                site_results.append((time.time(), status, seconds, timings))
                if not 200 <= status < 400 and len(site_failures) < 50:
                    site_failures.append({"t": round(time.time() - measurement_started, 1), "domain": domain,
                                          "status": status, "latency_ms": round(seconds * 1000, 1),
                                          "failed_phase": timings["failed_phase"],
                                          "phase_ms": {key: round(value * 1000, 1) if value is not None else None
                                                       for key, value in timings.items() if key.endswith("_s")},
                                          "runtime_evidence": runtime_evidence,
                                          "detail": detail})

    def admin_probe():
        probe = Panel()
        probe.jar, probe.opener, probe.csrf = panel.jar, panel.opener, panel.csrf
        paths = ["/api/sites", "/api/jobs", "/readyz", "/api/sites/overview", "/api/accounts"]
        next_metrics_scrape = 0
        while not stop.is_set():
            for path in paths:
                begin = time.perf_counter()
                try:
                    status, body = probe.call("GET", path, timeout=60)
                except OSError as error:
                    status, body = 0, str(error)
                with lock:
                    admin_results.setdefault(path, []).append((time.time(), status, time.perf_counter() - begin))
                    if not 200 <= status < 400 and len(admin_failures) < 50:
                        admin_failures.append({"t": round(time.time() - measurement_started, 1), "path": path,
                                               "status": status, "detail": failure_detail(body)})
            if time.time() >= next_metrics_scrape:
                metrics_started = time.perf_counter()
                try:
                    status, body = probe.call("GET", "/metrics", timeout=60)
                except OSError as error:
                    status, body = 0, str(error)
                sample = {"t": round(time.time() - measurement_started, 1), "status": status,
                          "latency_ms": round((time.perf_counter() - metrics_started) * 1000, 1)}
                if status == 200:
                    sample["metrics"] = parse_site_runtime_metrics(body)
                    with lock:
                        latest_runtime_metrics["sampled_at"] = time.time()
                        latest_runtime_metrics["body"] = body
                else:
                    sample["detail"] = failure_detail(body)
                with lock:
                    runtime_metric_samples.append(sample)
                next_metrics_scrape = time.time() + 15
            stop.wait(0.5)

    # A static file through the same web server and TLS path but no PHP. If
    # it stalls together with the PHP pages, the stall is in the web server
    # or host; if it does not, it is in PHP-FPM.
    static_path = "/stepanel-static-probe.txt"
    static_file = os.path.join(static_docroot, static_path.lstrip("/"))
    with open(static_file, "w", encoding="utf-8") as handle:
        handle.write("static probe\n")
    owner = os.stat(static_docroot)
    os.chown(static_file, owner.st_uid, owner.st_gid)
    os.chmod(static_file, 0o644)
    static_results = []

    def static_probe():
        while not stop.is_set():
            status, seconds, _, timings = site_get(domains[0], static_path)
            with lock:
                static_results.append((time.time(), status, seconds, timings))
            stop.wait(1)

    customer_sessions = []
    for username in list(account_plan)[:max(CUSTOMER_PROBES, 0)]:
        session = Panel()
        password, secret = account_credentials[username]
        session.login(username, password, secret, fresh_window=False)
        customer_sessions.append((username, session))

    def customer_probe(username, session):
        owned = account_plan[username]
        while not stop.is_set():
            for path, key in (("/api/account/hosting", "sites"), ("/api/sites/overview", "sites")):
                begin = time.perf_counter()
                try:
                    status, body = session.call("GET", path, timeout=60)
                except OSError as error:
                    status, body = 0, str(error)
                elapsed = time.perf_counter() - begin
                violation = None
                if status == 200 and isinstance(body, dict):
                    violation = isolation_violation(owned, [item.get("site") for item in body.get(key) or []])
                with lock:
                    customer_results.setdefault(path, []).append((time.time(), status, elapsed))
                    if not 200 <= status < 400 and len(customer_failures) < 50:
                        customer_failures.append({"t": round(time.time() - measurement_started, 1), "account": username,
                                                  "path": path, "status": status, "detail": failure_detail(body)})
                    if violation and len(isolation_failures) < 50:
                        isolation_failures.append({"t": round(time.time() - measurement_started, 1), "account": username,
                                                   "path": path, "difference": violation})
            stop.wait(1)

    threads = [threading.Thread(target=site_client, daemon=True) for _ in range(CLIENTS)]
    threads.extend(threading.Thread(target=customer_probe, args=probe, daemon=True) for probe in customer_sessions)
    threads.append(threading.Thread(target=admin_probe, daemon=True))
    threads.append(threading.Thread(target=static_probe, daemon=True))
    restart = {"started": None, "ready": None, "error": None}

    def restart_under_load():
        if stop.wait(DURATION * RESTART_AT):
            return
        units = ["stepanel.service"]
        if subprocess.run(["systemctl", "is-enabled", "--quiet", "stepanel-worker.service"]).returncode == 0:
            units.append("stepanel-worker.service")
        log(f"restarting {' '.join(units)} under load")
        restart["started"] = time.time()
        result = subprocess.run(["systemctl", "restart", *units], capture_output=True, text=True)
        if result.returncode != 0:
            restart["error"] = f"systemctl restart failed: {result.stderr.strip()[:300]}"
            return
        deadline = time.time() + 300
        while time.time() < deadline:
            try:
                ready_status, _ = Panel().call("GET", "/readyz", timeout=10)
            except OSError:
                ready_status = 0
            if 200 <= ready_status < 400:
                restart["ready"] = time.time()
                log(f"ready again {round(restart['ready'] - restart['started'], 1)}s after the restart")
                return
            time.sleep(0.5)
        restart["error"] = "the host was not ready 300s after the restart"

    if RESTART:
        threads.append(threading.Thread(target=restart_under_load, daemon=True))
    for thread in threads:
        thread.start()

    time.sleep(DURATION / 4)
    log("starting two heavy backups")
    heavy_jobs = []
    for name in heavy:
        enqueued = time.time()
        status, response = panel.call("POST", "/api/backups", {"site": name, "include_databases": False})
        if status not in (200, 202):
            raise SystemExit(f"heavy backup {name}: HTTP {status} {response!r}")
        heavy_jobs.append({"site": name, "job": response["job_id"], "enqueued": enqueued})
    heavy_window[0] = time.time()
    for job in heavy_jobs:
        while "running_at" not in job:
            status, state = panel.call("GET", "/api/jobs/" + job["job"])
            if status == 200 and state.get("state") != "queued":
                job["running_at"] = time.time()
                break
            time.sleep(0.25)
    for job in heavy_jobs:
        final = panel.wait_job(job["job"], limit=DURATION * 4, poll=0.5)
        job["finished_at"] = time.time()
        job["state"] = final.get("state")
    heavy_window[1] = time.time()
    remaining = DURATION - (time.time() - measurement_started)
    if remaining > 0:
        time.sleep(remaining)
    stop.set()
    for thread in threads:
        thread.join(timeout=30)
    sampler.join(timeout=10)

    restart_windows = []
    if restart["started"] is not None:
        restart_windows.append((restart["started"], (restart["ready"] or time.time()) + RESTART_SETTLE_S))
    scheduled_count, scheduled_problems = 0, []
    if scheduled_sites:
        for name in scheduled_sites:
            status, _ = panel.call("DELETE", "/api/backup-schedules?site=" + urllib.parse.quote(name))
            if not 200 <= status < 300:
                scheduled_problems.append(f"{name}: could not remove the schedule (HTTP {status})")
        # Jobs straddling the restart must still finish; none may be lost.
        settle_deadline = time.time() + 600
        while True:
            status, listing = panel.call("GET", "/api/jobs", timeout=60)
            jobs = listing.get("jobs", []) if status == 200 and isinstance(listing, dict) else []
            pending = [job for job in jobs if job.get("kind") == "site.backup" and job.get("user") in scheduled_sites
                       and job.get("state") in ("queued", "running")]
            if status == 200 and not pending:
                break
            if time.time() >= settle_deadline:
                scheduled_problems.append(f"{len(pending)} scheduled backup(s) were still queued or running 600s after measurement")
                break
            time.sleep(2)
        scheduled_count, findings = scheduled_job_findings(jobs, scheduled_sites, measurement_started,
                                                           SCHEDULE_MINUTES * 60 * 0.5)
        scheduled_problems.extend(findings)

    lock_errors = subprocess.run(
        ["journalctl", "--since", since, "-u", "stepanel", "-u", "stepanel-worker", "--no-pager", "-q"],
        capture_output=True, text=True).stdout
    fpm_errors = fpm_log_errors(fpm_offsets, fpm_since)
    lock_error_count = sum(1 for line in lock_errors.splitlines() if "database is locked" in line or "SQLITE_BUSY" in line)

    def window(results, inside):
        begin, end = heavy_window
        return window_latencies(results, inside, begin, end)

    def phase_window(results, inside):
        begin, end = heavy_window
        return [timings for (t, _status, _seconds, timings) in results
                if ((begin <= t <= end) == inside)]

    def summarize_phases(results):
        phases = {}
        for key in ("client_setup_s", "tcp_connect_s", "tls_handshake_s",
                    "time_to_first_byte_s", "response_transfer_s", "total_s"):
            measured = [item[key] for item in results if item.get(key) is not None]
            phases[key.removesuffix("_s")] = summarize(measured)
        return phases

    report = {
        "profile": PROFILE, "sites": SITES, "domains": len(domains), "accounts": len(account_plan),
        "customer_probes": len(customer_sessions), "duration_s": DURATION, "site_clients": CLIENTS,
        "heavy_mib": HEAVY_MIB, "started_utc": started_at, "setup_s": setup_seconds,
        "host": {"kernel": platform.release(), "machine": platform.machine(),
                 "logical_cpus": os.cpu_count()},
        "site_requests": {
            "total": len(site_results),
            "timeline_5s": request_timeline(site_results, measurement_started),
            "static_probe": {
                "path": static_path,
                "total": len(static_results),
                "errors": sum(1 for (_, status, _, _) in static_results if not 200 <= status < 400),
                "timeline_5s": request_timeline(static_results, measurement_started),
            },
            "errors": sum(1 for (_, status, _, _) in site_results if not 200 <= status < 400),
            "status_counts": dict(sorted(Counter(status for (_, status, _, _) in site_results).items())),
            "transport_failure_phase_counts": dict(sorted(Counter(
                timings["failed_phase"] for (_, status, _, timings) in site_results
                if status == 0).items())),
            "error_samples": site_failures,
            "baseline": summarize(window(site_results, False)),
            "during_heavy": summarize(window(site_results, True)),
            "baseline_outcomes": window_status_counts(
                site_results, False, *heavy_window),
            "during_heavy_outcomes": window_status_counts(
                site_results, True, *heavy_window),
            "phase_latency_baseline": summarize_phases(phase_window(site_results, False)),
            "phase_latency_during_heavy": summarize_phases(phase_window(site_results, True)),
        },
        "admin": {path: {
            "errors": sum(1 for (_, status, _) in results if not 200 <= status < 400),
            "baseline": summarize(window(results, False)),
            "during_heavy": summarize(window(results, True)),
        } for path, results in admin_results.items()},
        "customer": {path: {
            "errors": sum(1 for (_, status, _) in results if not 200 <= status < 400),
            "baseline": summarize(window(results, False)),
            "during_heavy": summarize(window(results, True)),
        } for path, results in customer_results.items()},
        "customer_failures": customer_failures,
        "tenant_isolation_failures": isolation_failures,
        "restart_under_load": {
            "enabled": RESTART,
            "ready_after_s": round(restart["ready"] - restart["started"], 1) if restart["ready"] and restart["started"] else None,
            "error": restart["error"],
            "admin_errors_in_window": sum(1 for results in admin_results.values() for (t, status, _) in results
                                          if not 200 <= status < 400 and any(b <= t <= e for b, e in restart_windows)),
            "customer_errors_in_window": sum(1 for results in customer_results.values() for (t, status, _) in results
                                             if not 200 <= status < 400 and any(b <= t <= e for b, e in restart_windows)),
        },
        "scheduled_jobs": {
            "sites": len(scheduled_sites), "interval_minutes": SCHEDULE_MINUTES,
            "rate_per_hour": len(scheduled_sites) * 60 // SCHEDULE_MINUTES if SCHEDULE_MINUTES else 0,
            "completed_in_window": scheduled_count, "problems": scheduled_problems,
        },
        "site_runtime_diagnostics": {
            **summarize_runtime_metrics(runtime_metric_samples),
            "samples": runtime_metric_samples,
        },
        "heavy_operations": [{
            "site": job["site"], "state": job.get("state"),
            "queue_wait_s": round(job.get("running_at", job["enqueued"]) - job["enqueued"], 2),
            "duration_s": round(job.get("finished_at", 0) - job.get("running_at", job["enqueued"]), 1),
        } for job in heavy_jobs],
        "resources": {
            key: {"max": max(s[key] for s in sampler.samples), "mean": round(sum(s[key] for s in sampler.samples) / len(sampler.samples), 1)}
            for key in ("cpu_pct", "panel_worker_rss_mib", "php_fpm_rss_mib", "mariadb_rss_mib", "disk_read_mib_s", "disk_write_mib_s",
                        "fpm_master_fds", "fpm_master_fd_limit", "fpm_children")
        } if sampler.samples else {},
        "min_mem_available_mib": min((s["mem_available_mib"] for s in sampler.samples), default=None),
        "control_plane_lock_errors": lock_error_count,
        "fpm_log_errors": fpm_errors,
        "admin_failures": admin_failures,
    }
    os.makedirs(os.path.dirname(REPORT), exist_ok=True)
    with open(REPORT, "w") as handle:
        json.dump(report, handle, indent=2)

    print(f"# Capacity profile `{PROFILE}`: {SITES} sites, {len(domains)} domains, {len(account_plan)} customer accounts\n")
    print(f"Started {started_at} UTC; setup {setup_seconds}s; {DURATION}s measured with {CLIENTS} site clients; two {HEAVY_MIB} MiB backups started at {DURATION // 4}s.\n")
    print("| Traffic | Window | Requests | p50 ms | p95 ms | p99 ms | max ms | Errors |")
    print("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |")
    rows = ([("site pages", report["site_requests"], site_results)]
            + [(path, data, admin_results[path]) for path, data in report["admin"].items()]
            + [(f"customer {path}", data, customer_results[path]) for path, data in report["customer"].items()])
    for name, data, results in rows:
        for label in ("baseline", "during_heavy"):
            stats = data[label]
            outcomes = window_status_counts(results, label == "during_heavy", *heavy_window)
            print(f"| {name} | {label.replace('_', ' ')} | {stats['count']} | {stats['p50_ms']} | {stats['p95_ms']} | {stats['p99_ms']} | {stats['max_ms']} | {outcomes['errors']} |")
    print("\n| Heavy operation | State | Queue wait s | Duration s |")
    print("| --- | --- | ---: | ---: |")
    for job in report["heavy_operations"]:
        print(f"| backup {job['site']} | {job['state']} | {job['queue_wait_s']} | {job['duration_s']} |")
    print("\n| Resource | Mean | Max |")
    print("| --- | ---: | ---: |")
    for key, values in report["resources"].items():
        print(f"| {key} | {values['mean']} | {values['max']} |")
    for label in ("baseline", "during_heavy"):
        print(f"\n| Site-request phase latency, {label.replace('_', ' ')} | p50 ms | p95 ms | p99 ms | max ms | samples |")
        print("| --- | ---: | ---: | ---: | ---: | ---: |")
        for phase, stats in report["site_requests"][f"phase_latency_{label}"].items():
            print(f"| {phase} | {stats['p50_ms']} | {stats['p95_ms']} | {stats['p99_ms']} | {stats['max_ms']} | {stats['count']} |")
    print(f"\nSite transport failure phases: {report['site_requests']['transport_failure_phase_counts']}")
    timeline = report["site_requests"]["timeline_5s"]
    stalled = sum(1 for _, ok, failed in timeline if failed and ok <= 1)
    static = report["site_requests"]["static_probe"]
    print(f"\nSite request timeline: {stalled} of {len(timeline)} five-second windows had failures with at most one success. "
          f"Static file probe (no PHP, {static['path']}): {static['errors']} of {static['total']} requests failed.")
    print(f"\nMinimum available memory: {report['min_mem_available_mib']} MiB. Control-plane database-lock errors in the panel and worker journals: {lock_error_count}.")
    fpm_resources = report["resources"]
    if "fpm_master_fds" in fpm_resources:
        print(f"\nPHP-FPM master descriptors: peak {fpm_resources['fpm_master_fds']['max']} of a {fpm_resources['fpm_master_fd_limit']['max']} limit; peak live children {fpm_resources['fpm_children']['max']}.")
    print(f"PHP-FPM master warnings during measurement: {fpm_errors['counts']}")
    for line in fpm_errors["examples"]:
        print(f"- `{line}`")
    runtime = report["site_runtime_diagnostics"]
    peak = runtime["peak"]
    print("\n| Site runtime diagnostics | Value |")
    print("| --- | ---: |")
    print(f"| /metrics scrapes (failed) | {runtime['scrapes']} ({runtime['failed_scrapes']}) |")
    print(f"| Peak sampled sites / available FPM pools | {peak['sites_sampled']} / {peak['fpm_available_sites']} |")
    print(f"| Peak saturated pools / listen queue | {peak['saturated_sites']} / {peak['max_listen_queue']} |")
    print(f"| Peak active workers / slow requests | {peak['max_active_workers']} / {peak['max_slow_requests']} |")
    print(f"| Peak site CPU quota / memory limit use | {peak['max_site_cpu_quota_utilization_pct']}% / {peak['max_site_memory_limit_utilization_pct']}% |")
    print(f"| Oldest FPM sample age | {peak['oldest_fpm_sample_age_s']} s |")
    runtime_diagnostics = report["site_runtime_diagnostics"]
    print(f"| Maximum sites without an FPM sample | {runtime_diagnostics['max_sites_without_fpm_sample']} |")
    if runtime_diagnostics["fpm_unavailable_site_samples"]:
        print(f"| FPM-unavailable site examples (up to 25) | {', '.join(runtime_diagnostics['fpm_unavailable_site_samples'])} |")
    if runtime_diagnostics["saturated_site_samples"]:
        print(f"| Saturated site examples (up to 25) | {', '.join(runtime_diagnostics['saturated_site_samples'])} |")
    if site_failures:
        print("\nSite HTTP failures (first 20):")
        for failure in site_failures[:20]:
            print(f"- t={failure['t']}s {failure['domain']} HTTP {failure['status']} ({failure['latency_ms']} ms; phase={failure['failed_phase']}; timings={failure['phase_ms']}): {failure['detail'][:300].replace('|', '/')}")
    if admin_failures:
        print("\n| Admin failure at s | Path | Status | Detail |")
        print("| ---: | --- | ---: | --- |")
        for failure in admin_failures[:20]:
            print(f"| {failure['t']} | {failure['path']} | {failure['status']} | {failure['detail'][:200].replace('|', '/')} |")
    print(f"\nTenant isolation: {len(customer_sessions)} customer probe(s); {len(isolation_failures)} listing(s) differed from the account's own sites.")
    for failure in isolation_failures[:20]:
        print(f"- t={failure['t']}s {failure['account']} {failure['path']}: {failure['difference']}")
    if customer_failures:
        print("\n| Customer failure at s | Account | Path | Status | Detail |")
        print("| ---: | --- | --- | ---: | --- |")
        for failure in customer_failures[:20]:
            print(f"| {failure['t']} | {failure['account']} | {failure['path']} | {failure['status']} | {failure['detail'][:200].replace('|', '/')} |")
    restart_report = report["restart_under_load"]
    if RESTART:
        print(f"\nRestart under load: ready again after {restart_report['ready_after_s']} s; "
              f"{restart_report['admin_errors_in_window']} admin and {restart_report['customer_errors_in_window']} customer probe failure(s) inside the restart window"
              + (f"; error: {restart_report['error']}" if restart_report["error"] else ""))
    scheduled_report = report["scheduled_jobs"]
    if scheduled_sites:
        print(f"\nScheduled jobs: {scheduled_report['sites']} sites every {SCHEDULE_MINUTES} min ({scheduled_report['rate_per_hour']}/hour); "
              f"{scheduled_count} scheduled backup(s) ran during measurement; {len(scheduled_problems)} problem(s).")
        for problem in scheduled_problems[:20]:
            print(f"- {problem}")
    admin_errors = sum(errors_outside_windows(results, restart_windows) for results in admin_results.values())
    customer_errors = sum(errors_outside_windows(results, restart_windows) for results in customer_results.values())
    failed = (report["site_requests"]["errors"] or admin_errors or customer_errors or isolation_failures
              or any(j["state"] != "completed" for j in report["heavy_operations"])
              or restart_report["error"] or scheduled_problems)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
