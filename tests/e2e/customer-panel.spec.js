// Customer panel (web/panel.html) journey against a disposable seeded
// instance: an administrator provisions a customer, the customer signs in
// with an authenticator code and lands on /panel, and every view renders and
// passes axe. File manager responses are stubbed because the disposable host
// has no hosting-account sites; the server side of the file manager is
// covered by site_files_test.go and deploy/lab/filectl-smoke.sh.
const crypto = require('crypto');
const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;

const enabled = Boolean(process.env.STEPANEL_E2E_BASE_URL);
const adminUser = process.env.STEPANEL_E2E_USERNAME || 'admin';
const adminPassword = process.env.STEPANEL_E2E_PASSWORD;
const customer = { username: 'e2ecustomer', password: 'E2E-Customer-Password-1234', secret: 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP' };

function totp(secret) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = '';
  for (const char of secret) bits += alphabet.indexOf(char).toString(2).padStart(5, '0');
  const key = Buffer.from(bits.match(/.{8}/g).map((byte) => parseInt(byte, 2)));
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const digest = crypto.createHmac('sha1', key).update(counter).digest();
  const offset = digest[digest.length - 1] & 15;
  return String((digest.readUInt32BE(offset) & 0x7fffffff) % 1e6).padStart(6, '0');
}

async function checkA11y(page) {
  const result = await new AxeBuilder({ page }).analyze();
  expect(result.violations, result.violations.map((violation) => `${violation.id}: ${violation.help}`).join('\n')).toEqual([]);
}

function hostingFor(username) {
  return {
    account: username,
    unix_user: username,
    home: `/home/${username}`,
    sites: [
      { site: 'e2e-main', placed: true, kind: 'main', directory: 'public_html', doc_root: `/home/${username}/public_html` },
      { site: 'e2e-shop', placed: true, kind: 'addon', directory: 'shop.example.org', doc_root: `/home/${username}/shop.example.org` },
    ],
  };
}

test('customer panel: views, file manager and accessibility', async ({ browser }, testInfo) => {
  test.skip(!enabled || !adminPassword, 'Set STEPANEL_E2E_BASE_URL and STEPANEL_E2E_PASSWORD for a disposable seeded instance');
  const baseURL = process.env.STEPANEL_E2E_BASE_URL;
  // Keep repeated/parallel Playwright attempts isolated in the shared test
  // control plane rather than racing to create the same customer account.
  const testCustomer = {
    ...customer,
    username: `${customer.username}-w${testInfo.workerIndex}-r${testInfo.repeatEachIndex}`,
  };
  const hosting = hostingFor(testCustomer.username);

  // Provision the customer as the administrator.
  const admin = await browser.newContext({ baseURL });
  const adminPage = await admin.newPage();
  await adminPage.goto('/login');
  await adminPage.getByLabel('Username').fill(adminUser);
  await adminPage.getByLabel('Password', { exact: true }).fill(adminPassword);
  if (process.env.STEPANEL_E2E_TOTP) await adminPage.getByLabel('Authenticator code').fill(process.env.STEPANEL_E2E_TOTP);
  await adminPage.getByRole('button', { name: 'Sign in' }).click();
  await expect(adminPage).not.toHaveURL(/\/login$/);
  const csrf = decodeURIComponent((await admin.cookies()).find((cookie) => cookie.name === 'stepanel_csrf').value);
  const created = await adminPage.request.post('/api/accounts', {
    headers: { 'X-CSRF-Token': csrf },
    data: { username: testCustomer.username, password: testCustomer.password, totp_secret: testCustomer.secret, plan: 'professional', sites: [] },
  });
  expect([201, 409]).toContain(created.status());
  await admin.close();

  const context = await browser.newContext({ baseURL });
  const page = await context.newPage();
  await page.route('**/api/account/hosting', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(hosting) }));
  await page.route('**/api/sites/overview', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sites: [
    { site: 'e2e-main', routes: [{ site: 'e2e-main', domain: 'main.example.org' }], database_count: 1, kind: 'main' },
    { site: 'e2e-shop', routes: [], database_count: 0, kind: 'addon' },
  ] }) }));
  const writes = [];
  await page.route('**/api/sites/files/e2e-main**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (request.method() !== 'GET') {
      writes.push(`${request.method()} ${url.pathname}${url.search}`);
      return route.fulfill({ status: 200, contentType: 'application/json', body: '{"ok":true}' });
    }
    if (url.pathname.endsWith('/content')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: url.searchParams.get('path'), size: 21, text: true, content: '<?php echo "hello";\n' }) });
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: url.searchParams.get('path') || '', truncated: false, entries: [
      { name: 'wp-content', type: 'dir', size: 4096, mode: '2750', mod_time: '2026-10-01T12:00:00Z' },
      { name: 'index.php', type: 'file', size: 21, mode: '640', mod_time: '2026-10-01T12:00:00Z' },
    ] }) });
  });

  await page.goto('/login');
  await page.getByLabel('Username').fill(testCustomer.username);
  await page.getByLabel('Password', { exact: true }).fill(testCustomer.password);
  await page.getByLabel('Authenticator code').fill(totp(testCustomer.secret));
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/panel/);

  // Home: the account and its websites, with no operator navigation.
  await expect(page.getByRole('heading', { name: `Welcome back, ${testCustomer.username}` })).toBeVisible();
  await expect(page.locator('#panelHome')).toHaveText(`/home/${testCustomer.username}`);
  await expect(page.locator('#panelSiteList')).toContainText(`/home/${testCustomer.username}/public_html`);
  await expect(page.getByRole('link', { name: 'Customers' })).toHaveCount(0);
  await checkA11y(page);

  // Files: browse, open, edit and save.
  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Files' }).click();
  await expect(page.locator('#files')).toBeVisible();
  await expect(page.getByRole('button', { name: 'index.php', exact: true })).toBeVisible();
  await checkA11y(page);
  await page.getByRole('button', { name: 'index.php', exact: true }).click();
  await expect(page.locator('#fileEditorText')).toHaveValue('<?php echo "hello";\n');
  await page.locator('#fileEditorText').fill('<?php echo "edited";\n');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('#fileEditorStatus')).toContainText('Saved index.php');
  expect(writes).toContain('PUT /api/sites/files/e2e-main/content?path=index.php');
  await page.getByRole('button', { name: 'wp-content', exact: true }).click();
  await expect(page.locator('#fileBreadcrumbs [aria-current="location"]')).toHaveText('wp-content');

  // Domains: connected domains and a link into the site's Domains tab.
  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Domains & SSL' }).click();
  await expect(page.locator('#panelDomainRows')).toContainText('main.example.org');
  await checkA11y(page);

  for (const [name, id] of [['Websites', '#websites'], ['Databases & backups', '#data'], ['PHP, cron & logs', '#runtime'], ['Account', '#account'], ['Activity', '#activity']]) {
    await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name }).click();
    await expect(page.locator(id)).toBeVisible();
    await checkA11y(page);
  }
  await context.close();
});
