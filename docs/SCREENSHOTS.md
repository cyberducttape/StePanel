# Product preview

The assets in this document are synchronized with the current development
administrator dashboard (v0.7.0), including the managed-sites workspace,
resource posture, Security Center, deployment records, and recovery workflows.
They are deterministic product illustrations, not captures from a live host;
values are representative and the real dashboard renders service, site,
database, capability, job state, and user role from the configured server.

**Note:** The preview images may show version numbers for demonstration purposes.
These are synthetic illustrations; the actual version should match the current
repository version. When updating previews for future releases, ensure version
numbers align with the tagged release.

## Operator workspace

![StePanel operator workspace preview](assets/operator-workspace-preview.png)

This is the primary product overview: resource posture, Security Center,
deployment status, restore-to-staging, and site workspace operations. Values are
synthetic and do not represent a live host.

## Developer workspace

![Developer workspace preview](assets/developer-workspace-preview.png)

This is an illustrative product mockup for the new developer workflows, not a
capture from a live host. It shows the intended workspace organization for PHP
runtime profiles, encrypted environment metadata, Composer, the Podman build
runner, staging, logs, workers, deploy keys, and scheduled tasks. Each card
maps to authenticated API and helper behavior documented in
[Developer workflows](DEVELOPER_WORKFLOWS.md).

## Dashboard

![StePanel dashboard workspace preview](assets/dashboard-workspace-preview.png)

This is the current dark, navigation-led dashboard shell. It matches the live
workspace hierarchy—operator context, managed-site cards, security posture,
and persistent jobs—rather than the retired light infrastructure layout. The
preview is rendered at 2× resolution for crisp display on GitHub and retina
screens. The editable [SVG source](assets/dashboard-workspace-preview.svg) remains
available for presentations and product materials.

## What the preview covers

<table>
<tr>
<td width="50%"><strong>Live operations</strong><br>Service inventory, health counts, load, uptime, and attention states come from the host rather than placeholder activity.</td>
<td width="50%"><strong>Safe migrations</strong><br>The migration center makes cpmove and WordPress restore workflows visible, reviewable, and asynchronous.</td>
</tr>
<tr>
<td><strong>Database operations</strong><br>The current dashboard identifies the engine and adds native database inventory, least-privilege provisioning, credential rotation, diagnostics, and safety-dump-protected deletion.</td>
<td><strong>Persistent jobs</strong><br>Restore, backup, certificate, and application work is represented by durable job state with status links.</td>
</tr>
<tr>
<td><strong>Security posture</strong><br>Operator checks, readiness, request correlation, and capability-aware controls are surfaced before changes are made.</td>
<td><strong>Developer workspace</strong><br>Managed sites group domains, runtime/environment controls, deployments, logs, workers, scheduled tasks, and verified recent activity.</td>
</tr>
<tr>
<td><strong>Resource and recovery operations</strong><br>Administrators can inspect desired/applied resource profiles, live cgroup counters, reconciliation state, bounded site usage, and restore verified files or managed database dumps locally, from off-site storage, or into isolated staging where supported.</td>
<td><strong>Security operations</strong><br>The read-only Security Center combines posture checks, service health, disk/inode pressure, and backup schedule health without implying firewall or patch automation.</td>
</tr>
</table>

This illustration mirrors the current development dashboard structure using
representative values, including Caddy as the selected webserver, automatic
HTTPS, the managed-sites workspace, database operations panel, and the
Apache-to-Caddy `.htaccess` migration entry point.
At runtime, all service counts, health states, load, security checks, and jobs
come from the current server; the application does not ship simulated activity.

## Customer workspace

The shared-hosting beta has a separate, role-aware customer workspace. It uses
the same professional navigation, responsive task cards, and accessible visual
system as the administrator dashboard, but it shows the customer's plan,
assigned-site count, assigned site cards, and matching backup/job activity.
Infrastructure, security, database, migration, cloud, SSH, and account
administration controls are intentionally absent. The server enforces the same
scope at the API boundary; this is not only a visual simplification.

The operator preview above is an administrator illustration, not a customer screenshot.
For release review, capture both roles from the tagged build with synthetic
data and verify the mobile layout as well as the role boundary. See
[`SHARED_HOSTING.md`](SHARED_HOSTING.md) for the supported customer scope.

For release reviews, capture a screenshot from the tagged build as a supplement
to this deterministic preview. Real screenshots are useful for verifying theme,
responsive layout, and capability-specific controls on a target host.
