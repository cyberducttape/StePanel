# Live UI screenshots

The images in [`SCREENSHOTS.md`](SCREENSHOTS.md) are real UI captures from a
disposable development control plane, produced by
`scripts/capture-screenshots.sh`. That host has no root broker, so web server,
account, and cgroup changes are stubbed. Release reviews should also capture
an installed host running the tagged build, so operators can see the actual
capability, role, theme, and responsive state with real helpers.

Use a disposable host or container with synthetic data only. Do not capture
real customer names, domains, credentials, tokens, source code, or IPs.

## Capture checklist

1. Check out the release tag and record its commit ID.
2. Start the application with a disposable state directory and demo operator
   and customer accounts.
3. Capture the administrator overview, Sites, Migrations, Deployments,
   Security, and Activity / Jobs views at desktop and mobile widths.
4. Capture the customer view separately and verify that infrastructure and
   other tenants are absent.
5. Record the browser, viewport, theme, seed/data version, tag, and commit ID
   beside the images.
6. Inspect the images for secrets and replace any host-specific values with
   synthetic data before publishing.

The capture is evidence of the tagged UI only; it does not replace the
production-readiness or load-test gates.

The CI browser gate also captures the seeded administrator overview at desktop
and mobile widths. The `stepanel-live-ui-screenshots` workflow artifact includes
those PNGs plus `metadata.txt` with the tested commit, UTC timestamp, browser,
and seed description. Review the artifact from the candidate commit before a
release; screenshots from an untagged or failed run are not release evidence.
