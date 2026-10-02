# StePanel Helm chart

The chart is a packaging example for StePlatform. Render it before applying:

```sh
helm template stepanel deploy/helm/stepanel
helm upgrade --install stepanel deploy/helm/stepanel \
  --set image.digest=sha256:23ac17a2092ce8153ff85dc7662883bedb018615b016586a3643f96d22cdd6d0
```

Create `stepanel-secrets` before installing; the pod fails with
`CreateContainerConfigError` if any key is missing:

```sh
kubectl -n stepanel create secret generic stepanel-secrets \
  --from-literal=admin-password='change-me' \
  --from-literal=session-secret="$(openssl rand -hex 32)" \
  --from-literal=audit-key="$(openssl rand -hex 32)" \
  --from-literal=admin-totp-secret='BASE32_SECRET' \
  --from-literal=account-key="$(openssl rand -hex 32)" \
  --from-literal=environment-key="$(openssl rand -hex 32)" \
  --from-literal=backup-signing-key="$(openssl rand -hex 32)" \
  --from-literal=offsite-target='rclone:remote/stepanel'
```

Back up `account-key`, `environment-key`, and `backup-signing-key` outside the
cluster. Production installations should pin an
image digest in `values.yaml`, select an appropriate persistent storage class, enable and
configure the ingress with TLS, and add a network policy appropriate to the
cluster ingress controller. The chart explicitly enables trusted upstream TLS
termination only when ingress is enabled and configured with TLS; do not expose
its Service directly. Label the ingress namespace `stepanel.ingress=true` or
override the selector. The chart enforces one replica because restore
jobs and managed site state are local to the control plane. Its control-plane
PVC also retains shared-hosting customer account state beside durable sessions;
back it up as encrypted sensitive state if customer accounts are provisioned.

The default values expect a `stepanel-tls` Secret for
`stepanel.example.com`; replace the host and secret name with the certificate
managed by your ingress controller.

The chart packages the control plane only. It does not grant access to a
Kubernetes node's Apache, PHP-FPM, systemd, accounts, or StePanel privileged
helpers, so host-provisioning endpoints are unavailable unless a separate,
explicit integration is supplied. The systemd deployment remains the full
host-management topology.
