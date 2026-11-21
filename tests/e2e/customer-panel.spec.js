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
  const pageErrors = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  await page.route('**/api/account/hosting', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(hosting) }));
  let customerBackups = [
    { site: 'e2e-main', verified_at: '2026-11-20T12:00:00Z' },
  ];
  await page.route('**/api/backups?limit=100', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ backups: customerBackups }) }));
  let overviewRequests = 0;
  let shopRouteConfigured = false;
  await page.route('**/api/sites/overview', (route) => {
    overviewRequests++;
    // sites.js loads the Websites grid first; fail the subsequent customer
    // overview request so this exercises panel.js's unavailable-state UI.
    if (overviewRequests === 2) {
      return route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":"overview unavailable for test"}' });
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sites: [
    { site: 'e2e-main', routes: [{ site: 'e2e-main', domain: 'main.example.org' }], database_count: 1, kind: 'main' },
    { site: 'e2e-shop', routes: shopRouteConfigured ? [{ site: 'e2e-shop', domain: 'shop.example.org' }] : [], database_count: 0, kind: 'addon' },
    ] }) });
  });
  const writes = [];
  const writeRevisions = [];
  const revisionA = `"sha256-${'a'.repeat(64)}"`;
  const revisionB = `"sha256-${'b'.repeat(64)}"`;
  let conflictOnMainWrite = true;
  let releaseMainRead;
  const mainReadPending = new Promise((resolve) => { releaseMainRead = resolve; });
  let releaseShopList;
  const shopListPending = new Promise((resolve) => { releaseShopList = resolve; });
  await page.route('**/api/sites/files/e2e-main**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (request.method() !== 'GET') {
      writes.push(`${request.method()} ${url.pathname}${url.search}`);
      writeRevisions.push(request.headers()['if-match'] || '');
      if (request.method() === 'PUT' && conflictOnMainWrite) {
        conflictOnMainWrite = false;
        return route.fulfill({ status: 409, contentType: 'text/plain', body: 'file changed since it was opened' });
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: '{"ok":true}' });
    }
    if (url.pathname.endsWith('/content')) {
      if (url.searchParams.get('path') === 'index.php' && !releaseMainRead.done) await mainReadPending;
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: url.searchParams.get('path'), size: 21, text: true, revision: revisionA, content: '<?php echo "hello";\n' }) });
    }
    if (url.searchParams.get('path') === 'wp-content' && !releaseShopList.done) await shopListPending;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: url.searchParams.get('path') || '', truncated: false, entries: [
      { name: 'wp-content', type: 'dir', size: 4096, mode: '2750', mod_time: '2026-10-01T12:00:00Z' },
      { name: 'index.php', type: 'file', size: 21, mode: '640', mod_time: '2026-10-01T12:00:00Z' },
    ] }) });
  });
  await page.route('**/api/sites/files/e2e-shop**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (request.method() !== 'GET') {
      writes.push(`${request.method()} ${url.pathname}${url.search}`);
      writeRevisions.push(request.headers()['if-match'] || '');
      return route.fulfill({ status: 200, contentType: 'application/json', body: '{"ok":true}' });
    }
    if (url.pathname.endsWith('/content')) {
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: url.searchParams.get('path'), size: 20, text: true, revision: revisionB, content: '<?php echo "shop";\n' }) });
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: url.searchParams.get('path') || '', truncated: false, entries: [
      { name: 'shop.php', type: 'file', size: 20, mode: '640', mod_time: '2026-10-01T12:00:00Z' },
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
  await expect(page.locator('#panelOnboardingList')).toContainText('Open your website workspace');
  await expect(page.locator('#panelOnboardingList')).toContainText('No integrity-verified backup is recorded for e2e-shop');
  await expect(page.locator('#panelOnboardingList').getByRole('link', { name: 'Review e2e-shop backups' })).toHaveAttribute('href', '#site=e2e-shop&tab=backups');
  await expect(page.locator('#panelOnboardingList')).toContainText('Domain routes could not be checked');
  await expect(page.locator('#panelOnboardingProgress')).toContainText('1 of 3 essentials complete');
  await expect(page.locator('#panelPrimaryAction')).toHaveAttribute('href', '#websites');
  await expect(page.locator('#panelSiteList')).toContainText('public_html');
  await expect(page.locator('#panelSiteList')).not.toContainText(`/home/${testCustomer.username}/public_html`);
  await expect(page.locator('#panelSitesStatus')).toContainText('could not be refreshed');
  await page.locator('#panelSitesStatus').getByRole('button', { name: 'Retry' }).click();
  await expect(page.locator('#panelSitesStatus')).toBeHidden();
  await expect(page.locator('#panelOnboardingList')).toContainText('e2e-shop has no configured domain route');
  await expect(page.locator('#panelPrimaryAction')).toHaveAttribute('href', '#site=e2e-shop&tab=domains');
  await expect(page.locator('#panelOnboardingProgress')).toContainText('1 of 3 essentials complete');
  shopRouteConfigured = true;
  await page.locator('#panelOnboardingRefresh').click();
  await expect(page.locator('#panelOnboardingList')).toContainText('Verify DNS and HTTPS');
  await expect(page.locator('#panelOnboardingList')).toContainText('does not prove public DNS resolves or HTTPS works');
  await expect(page.locator('#panelOnboardingList').getByRole('link', { name: 'Review DNS & HTTPS' })).toHaveAttribute('href', '#site=e2e-main&tab=domains');
  await expect(page.locator('#panelPrimaryAction')).toHaveAttribute('href', '#site=e2e-main&tab=domains');
  await expect(page.locator('#panelOnboardingProgress')).toContainText('1 of 3 essentials complete');
  customerBackups = [
    ...customerBackups,
    { site: 'e2e-shop', verified_at: '2026-11-20T13:00:00Z' },
  ];
  await page.locator('#panelOnboardingRefresh').click();
  await expect(page.locator('#panelOnboardingList')).toContainText('Every assigned website has an integrity-verified backup');
  await expect(page.locator('#panelOnboardingProgress')).toContainText('2 of 3 essentials complete');
  await expect(page.getByRole('link', { name: 'Customers' })).toHaveCount(0);
  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Websites' }).click();
  await expect(page.locator('#siteOverview')).toContainText('e2e-main');
  await page.locator('#siteOverview').getByRole('button', { name: 'Manage site' }).first().click();
  await expect(page.locator('#siteDetail')).toBeVisible();
  await expect(page.locator('#siteDetail')).toContainText('e2e-main workspace');
  await expect(page.locator('#siteDetail').getByRole('tablist', { name: 'e2e-main everyday sections' })).toBeVisible();
  await expect(page.locator('#siteDetail').getByRole('tab', { name: 'Overview' })).toBeVisible();
  await expect(page.locator('#siteDetail').getByRole('tab', { name: 'Runtime' })).toBeHidden();
  await page.locator('#siteDetail .workspace-advanced summary').click();
  await expect(page.locator('#siteDetail').getByRole('tab', { name: 'Runtime' })).toBeVisible();
  await page.evaluate(() => { location.hash = '#site=e2e-main&tab=databases'; });
  await expect(page.locator('#siteDetail .workspace-advanced')).toHaveJSProperty('open', true);
  await expect(page.locator('#siteDetail').getByRole('tab', { name: 'Databases' })).toHaveAttribute('aria-selected', 'true');
  await checkA11y(page);
  await expect.poll(() => pageErrors).toEqual([]);
  await page.locator('#siteDetail').getByRole('button', { name: 'Close' }).click();
  await expect(page.locator('#siteCreateTemplate option[value="wordpress"]')).toHaveCount(1);
  await page.locator('#siteCreateTemplate').selectOption('wordpress');
  await expect(page.locator('#wordpressCreateFields')).toBeVisible();
  await page.getByRole('button', { name: 'Generate password' }).click();
  const generatedPassword = await page.locator('#siteCreateAdminPassword').inputValue();
  expect(generatedPassword).toHaveLength(24);
  await page.getByRole('button', { name: 'Show', exact: true }).click();
  await expect(page.locator('#siteCreateAdminPassword')).toHaveAttribute('type', 'text');
  await page.getByRole('button', { name: 'Hide', exact: true }).click();
  await expect(page.locator('#siteCreateAdminPassword')).toHaveAttribute('type', 'password');
  await expect.poll(() => pageErrors).toEqual([]);
  await checkA11y(page);

  // Files: browse, open, edit and save.
  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Files' }).click();
  await expect(page.locator('#files')).toBeVisible();
  await expect(page.getByRole('button', { name: 'index.php', exact: true })).toBeVisible();
  await checkA11y(page);
  // Navigating clears old rows before the new directory response arrives.
  await page.getByRole('button', { name: 'wp-content', exact: true }).click();
  await expect(page.locator('#fileTable')).toBeHidden();
  await expect(page.getByRole('button', { name: 'index.php', exact: true })).toHaveCount(0);
  await expect(page.locator('#fileToolbar')).toHaveAttribute('aria-busy', 'true');
  await expect(page.locator('#fileToolbar')).toHaveJSProperty('inert', true);
  releaseShopList.done = true;
  releaseShopList();
  await expect(page.locator('#fileBreadcrumbs [aria-current="location"]')).toHaveText('wp-content');
  await page.getByRole('button', { name: 'index.php', exact: true }).click();
  await page.locator('#fileSite').selectOption('e2e-shop');
  await expect(page.getByRole('button', { name: 'shop.php', exact: true })).toBeVisible();
  releaseMainRead.done = true;
  releaseMainRead();
  await expect(page.locator('#fileEditor')).toBeHidden();
  await expect(page.locator('#fileEditorText')).not.toHaveValue('<?php echo "hello";\n');
  expect(writes.some((entry) => entry.startsWith('PUT /api/sites/files/e2e-shop/'))).toBe(false);
  await page.getByRole('button', { name: 'shop.php', exact: true }).click();
  await expect(page.locator('#fileEditorText')).toHaveValue('<?php echo "shop";\n');
  await page.locator('#fileEditorText').fill('<?php echo "changed shop";\n');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('#fileEditorStatus')).toContainText('Saved shop.php');
  expect(writes).toContain('PUT /api/sites/files/e2e-shop/content?path=shop.php');
  expect(writeRevisions).toContain(revisionB);
  await page.locator('#fileSite').selectOption('e2e-main');
  await expect(page.getByRole('button', { name: 'index.php', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'index.php', exact: true }).click();
  await expect(page.locator('#fileEditorText')).toHaveValue('<?php echo "hello";\n');
  await page.locator('#fileEditorText').fill('<?php echo "edited";\n');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('#fileEditorStatus')).toContainText('file changed since it was opened');
  await expect(page.getByRole('button', { name: 'Reload latest' })).toBeVisible();
  await page.getByRole('button', { name: 'Reload latest' }).click();
  await expect(page.locator('#fileEditorText')).toHaveValue('<?php echo "hello";\n');
  await page.locator('#fileEditorText').fill('<?php echo "edited";\n');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('#fileEditorStatus')).toContainText('Saved index.php');
  expect(writes).toContain('PUT /api/sites/files/e2e-main/content?path=index.php');
  expect(writeRevisions).toContain(revisionA);
  await page.getByRole('button', { name: 'wp-content', exact: true }).click();
  await expect(page.locator('#fileBreadcrumbs [aria-current="location"]')).toHaveText('wp-content');

  // Domains: connected domains and a link into the site's Domains tab.
  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Domains & SSL' }).click();
  await expect(page.locator('#panelDomainRows')).toContainText('main.example.org');
  await expect(page.locator('#panelDomainRows')).toContainText('public_html');
  await expect(page.locator('#panelDomainRows')).not.toContainText(`/home/${testCustomer.username}/public_html`);
  await checkA11y(page);

  for (const [name, id] of [['Websites', '#websites'], ['Databases & backups', '#data'], ['PHP, cron & logs', '#runtime'], ['Account', '#account'], ['Activity', '#activity']]) {
    await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name }).click();
    await expect(page.locator(id)).toBeVisible();
    await checkA11y(page);
  }
  await context.close();
});
