# Live UI screenshots

The images in [`SCREENSHOTS.md`](SCREENSHOTS.md) are deterministic product
previews, not evidence from a running installation. Release reviews should add
captures from the tagged build so operators can see the actual capability,
role, theme, and responsive state.

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
