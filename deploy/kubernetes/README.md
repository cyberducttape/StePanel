# Kubernetes deployment

> **Packaging boundary:** this is control-plane packaging for evaluation or
> limited integration mode. It is not a highly available deployment and it is
> not a replacement for the native systemd installation, which is required for
> full single-host host management.

Create the secret out-of-band, then apply the manifest:

```sh
kubectl -n stepanel create secret generic stepanel-secrets \
  --from-literal=admin-password='change-me' \
  --from-literal=session-secret="$(openssl rand -hex 32)" \
  --from-literal=audit-key="$(openssl rand -hex 32)" \
  --from-literal=admin-totp-secret='BASE32_SECRET' \
  --from-literal=account-key="$(openssl rand -hex 32)" \
  --from-literal=environment-key="$(openssl rand -hex 32)" \
  --from-literal=backup-signing-key="$(openssl rand -hex 32)" \
  --from-literal=backup-encryption-key="$(openssl rand -hex 32)" \
  --from-literal=offsite-target='rclone:remote/stepanel'
kubectl apply -f deploy/kubernetes/stepanel.yaml
```

The production manifest requires TOTP MFA and an offsite rclone target. Replace
the example values before applying. Configure an HTTPS ingress in a namespace
labelled `stepanel.ingress=true`; the manifest sets
`STEPANEL_TLS_TERMINATED=1` because ingress traffic is the only allowed pod
ingress path.

The account key encrypts customer TOTP secrets and sensitive durable job
payloads, the environment key encrypts site environment secrets, and the backup
signing key authenticates backups. Back all three up separately from the PVC;
state and backups written with them cannot be read without them. The image includes the rclone
client but no provider credentials; provide the rclone configuration through
your cluster secret-management system.

The manifest creates two 50Gi persistent volume claims for control-plane data
and site files. Adjust their size and storage class for the environment. It
uses one replica with a `Recreate` rollout because restore jobs and persistent
state are process-local. This is intentionally a single-node deployment: it
has no HA or automatic failover. There is deliberately no
`PodDisruptionBudget`; `minAvailable: 1` cannot preserve availability for one
replica and would make voluntary node drains fail without adding protection.
The pod runs as UID/GID 10001 with a read-only root
filesystem and no service-account token. It assumes an HTTPS ingress terminates
TLS before forwarding to the Service; label that ingress namespace
`stepanel.ingress=true`. The included NetworkPolicy denies other ingress
traffic, so do not expose the Service directly.

The control-plane claim also holds durable session and shared-hosting customer
account state. Back up that claim as encrypted sensitive data; account state
contains bcrypt password hashes and TOTP seeds and must never be mounted into a
customer-facing workload or exported in support artifacts.

This container is a control-plane packaging target, not a host-management
agent. Kubernetes pods do not have the host's Apache, PHP-FPM, systemd, local
accounts, or privileged StePanel helpers. Endpoints that provision host
services therefore remain unavailable in this topology. Use the systemd
installation for full host management; use Kubernetes only for explicitly
integrated remote services and persistent migration storage.
