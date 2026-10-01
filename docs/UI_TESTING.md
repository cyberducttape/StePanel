# UI testing

The browser suite uses Playwright and axe-core against a disposable seeded
StePanel instance. It is intentionally opt-in because the repository does not
contain test credentials or a safe demo server.

```sh
npm install
npx playwright install chromium
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
