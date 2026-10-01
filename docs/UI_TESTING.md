# UI testing

The browser suite uses Playwright and axe-core against a disposable seeded
StePanel instance. CI runs it on every push and pull request (the `ui-e2e`
job in `.github/workflows/ci.yml`), so it is a merge gate.

`scripts/e2e-ui-test.sh` starts a throwaway control plane under a temporary
root with generated test-only secrets, waits for `/livez`, and runs the suite:

```sh
go build -o stepanel .
npm ci
npx playwright install chromium
./scripts/e2e-ui-test.sh ./stepanel
```

On hosts where Playwright's bundled Chromium is unsupported, use an installed
Google Chrome instead with `PLAYWRIGHT_CHANNEL=chrome`.

To run against an instance you started yourself:

```sh
STEPANEL_E2E_BASE_URL=http://127.0.0.1:18090 \
STEPANEL_E2E_PASSWORD='disposable-password' \
npm run test:e2e
```

The suite covers login/TOTP, keyboard and appearance interactions, Sites,
cPanel migration, the durable Job Center and cancellation affordance, Node
deployments, WordPress restore, database lifecycle, and customer role
isolation. Set `STEPANEL_E2E_CUSTOMER_PASSWORD` to enable the second-context
isolation check. Never use production credentials or real customer data.

The axe check runs on the authenticated overview and should be extended to
each new first-class screen as screens are split from the current workspace.
