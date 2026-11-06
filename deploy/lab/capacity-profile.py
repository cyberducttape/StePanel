#!/usr/bin/env python3
"""Installed-host capacity profile for StePanel.

Runs on a disposable host where StePanel is installed (the lab install smoke
leaves the panel at 127.0.0.1:8090 with known lab credentials). It:

  1. creates N live PHP sites through the API, each with a route served by
     the real web server and PHP-FPM;
  2. for CAPACITY_DURATION seconds, sends customer traffic to the sites and
     continuously probes the administrative API;
  3. a quarter of the way in, starts two heavy customer operations at once
     (backups of large incompressible sites) and measures their queue wait
     and duration;
  4. samples CPU, memory, disk I/O, process RSS, and control-plane
     database-lock errors throughout;

and prints a Markdown report (also written as JSON next to it). Profiles:
small (25 sites), node (150), large (500); CAPACITY_SITES overrides.

This produces measurements for one host and run, not a certified limit.
Lab only: it creates and leaves sites named cap-* and cap-heavy-*.
"""

import base64
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
import urllib.error
import urllib.parse
import urllib.request

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

    def login(self):
        # A fresh TOTP window, so an earlier lab login's code is not replayed.
        time.sleep(31 - int(time.time()) % 30)
        self.opener.open(PANEL + "/login", timeout=10).read()
        body = urllib.parse.urlencode({"username": USER, "password": PASSWORD, "totp": totp(TOTP_SECRET)}).encode()
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


def site_get(domain):
    """GET / from the site through the local web server; returns (status, seconds)."""
    started = time.perf_counter()
    try:
        context = ssl.create_default_context()
        context.check_hostname = False
        context.verify_mode = ssl.CERT_NONE
        with socket.create_connection(("127.0.0.1", 443), timeout=15) as raw:
            with context.wrap_socket(raw, server_hostname=domain) as tls:
                tls.sendall(f"GET / HTTP/1.1\r\nHost: {domain}\r\nConnection: close\r\n\r\n".encode())
                head = tls.recv(64)
                while tls.recv(65536):
                    pass
        status = int(head.split(b" ", 2)[1])
    except Exception:
        status = 0
    return status, time.perf_counter() - started


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


WHOLE_DISK = re.compile(r"^(vd[a-z]+|sd[a-z]+|xvd[a-z]+|nvme[0-9]+n[0-9]+)$")


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
            self.samples.append({
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
    log(f"profile {PROFILE}: {SITES} sites, {DURATION}s measurement, {CLIENTS} site clients, two {HEAVY_MIB} MiB heavy backups")

    setup_started = time.time()
    domains = []
    for index in range(SITES):
        name = f"cap-{index:03d}"
        domain = f"{name}.localhost"
        create_site(panel, name, domain)
        domains.append(domain)
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
    setup_seconds = round(time.time() - setup_started)
    log(f"setup finished in {setup_seconds}s")

    stop = threading.Event()
    sampler = Sampler(stop)
    sampler.start()
    lock = threading.Lock()
    site_results, admin_results, admin_failures = [], {}, []
    measurement_started = time.time()
    heavy_window = [None, None]

    def site_client():
        while not stop.is_set():
            status, seconds = site_get(random.choice(domains))
            with lock:
                site_results.append((time.time(), status, seconds))

    def admin_probe():
        probe = Panel()
        probe.jar, probe.opener, probe.csrf = panel.jar, panel.opener, panel.csrf
        paths = ["/api/sites", "/api/jobs", "/readyz", "/api/sites/overview"]
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
            stop.wait(0.5)

    threads = [threading.Thread(target=site_client, daemon=True) for _ in range(CLIENTS)]
    threads.append(threading.Thread(target=admin_probe, daemon=True))
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

    lock_errors = subprocess.run(
        ["journalctl", "--since", since, "-u", "stepanel", "-u", "stepanel-worker", "--no-pager", "-q"],
        capture_output=True, text=True).stdout
    lock_error_count = sum(1 for line in lock_errors.splitlines() if "database is locked" in line or "SQLITE_BUSY" in line)

    def window(results, inside):
        begin, end = heavy_window
        return [s for (t, status, s) in results if 200 <= status < 400 and ((begin <= t <= end) == inside)]

    report = {
        "profile": PROFILE, "sites": SITES, "duration_s": DURATION, "site_clients": CLIENTS,
        "heavy_mib": HEAVY_MIB, "started_utc": started_at, "setup_s": setup_seconds,
        "site_requests": {
            "total": len(site_results),
            "errors": sum(1 for (_, status, _) in site_results if not 200 <= status < 400),
            "baseline": summarize(window(site_results, False)),
            "during_heavy": summarize(window(site_results, True)),
        },
        "admin": {path: {
            "errors": sum(1 for (_, status, _) in results if not 200 <= status < 400),
            "baseline": summarize(window(results, False)),
            "during_heavy": summarize(window(results, True)),
        } for path, results in admin_results.items()},
        "heavy_operations": [{
            "site": job["site"], "state": job.get("state"),
            "queue_wait_s": round(job.get("running_at", job["enqueued"]) - job["enqueued"], 2),
            "duration_s": round(job.get("finished_at", 0) - job.get("running_at", job["enqueued"]), 1),
        } for job in heavy_jobs],
        "resources": {
            key: {"max": max(s[key] for s in sampler.samples), "mean": round(sum(s[key] for s in sampler.samples) / len(sampler.samples), 1)}
            for key in ("cpu_pct", "panel_worker_rss_mib", "php_fpm_rss_mib", "mariadb_rss_mib", "disk_read_mib_s", "disk_write_mib_s")
        } if sampler.samples else {},
        "min_mem_available_mib": min((s["mem_available_mib"] for s in sampler.samples), default=None),
        "control_plane_lock_errors": lock_error_count,
        "admin_failures": admin_failures,
    }
    os.makedirs(os.path.dirname(REPORT), exist_ok=True)
    with open(REPORT, "w") as handle:
        json.dump(report, handle, indent=2)

    print(f"# Capacity profile `{PROFILE}`: {SITES} sites\n")
    print(f"Started {started_at} UTC; setup {setup_seconds}s; {DURATION}s measured with {CLIENTS} site clients; two {HEAVY_MIB} MiB backups started at {DURATION // 4}s.\n")
    print("| Traffic | Window | Requests | p50 ms | p95 ms | p99 ms | max ms | Errors |")
    print("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |")
    rows = [("site pages", report["site_requests"])] + [(path, data) for path, data in report["admin"].items()]
    for name, data in rows:
        for label in ("baseline", "during_heavy"):
            stats = data[label]
            print(f"| {name} | {label.replace('_', ' ')} | {stats['count']} | {stats['p50_ms']} | {stats['p95_ms']} | {stats['p99_ms']} | {stats['max_ms']} | {data['errors'] if label == 'baseline' else ''} |")
    print("\n| Heavy operation | State | Queue wait s | Duration s |")
    print("| --- | --- | ---: | ---: |")
    for job in report["heavy_operations"]:
        print(f"| backup {job['site']} | {job['state']} | {job['queue_wait_s']} | {job['duration_s']} |")
    print("\n| Resource | Mean | Max |")
    print("| --- | ---: | ---: |")
    for key, values in report["resources"].items():
        print(f"| {key} | {values['mean']} | {values['max']} |")
    print(f"\nMinimum available memory: {report['min_mem_available_mib']} MiB. Control-plane database-lock errors in the panel and worker journals: {lock_error_count}.")
    if admin_failures:
        print("\n| Admin failure at s | Path | Status | Detail |")
        print("| ---: | --- | ---: | --- |")
        for failure in admin_failures[:20]:
            print(f"| {failure['t']} | {failure['path']} | {failure['status']} | {failure['detail'][:200].replace('|', '/')} |")
    failed = report["site_requests"]["errors"] or any(d["errors"] for d in report["admin"].values()) or any(j["state"] != "completed" for j in report["heavy_operations"])
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
