# Disposable end-to-end lab

This lab starts StePanel, MySQL, Prometheus, and Grafana. It is designed for
local demonstrations and migration testing, not production hosting.

The full lab requires a disposable VM or host that exposes a functional
systemd-compatible cgroup hierarchy. OpenVZ/LXC guests that do not expose
cgroups cannot run the systemd/resource-enforcement portion of this lab; use a
KVM/Cloud VM or a CI runner with nested container and cgroup support instead.

```sh
docker compose -f deploy/lab/docker-compose.yml up --build -d
bash deploy/lab/run.sh
```

Open:

- StePanel: <http://localhost:8080>
- Prometheus: <http://localhost:9090>
- Grafana: <http://localhost:3000>

Podman can build and run the individual images, but this repository does not
ship a Podman Compose wrapper. If `podman compose` is unavailable, use Docker
Compose on a cgroup-capable VM or translate the service definitions into an
equivalent Podman pod there. Do not disable cgroups to force the lab to run:
resource and systemd results would not be representative.

To submit the generated archive, use the dashboard or the API with
`confirm=IMPORT`. The lab contains no real credentials or customer data;
destroy it with `docker compose -f deploy/lab/docker-compose.yml down -v`.

## Recovery evidence

Run the repository-level recovery drills from the project root:

```sh
bash deploy/lab/run-recovery-drills.sh
```

The command writes a dated Markdown result under `docs/lab-results/`. These
tests use synthetic temporary data and document local contracts; they do not
replace the Docker/systemd failure-injection scenarios or a real disposable
host screenshot.

`install-smoke.sh` also runs `cpmove-import-smoke.sh`, which logs into the
installed panel, uploads a synthetic cpmove archive, waits for the durable
restore job, and verifies the imported file under `/var/www/sites/ci-import`.
Set `CPMOVE_SMOKE_SITE` to use a different disposable site name.
