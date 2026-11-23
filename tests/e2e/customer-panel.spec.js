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
  test.setTimeout(45_000);
  test.skip(!enabled || !adminPassword, 'Set STEPANEL_E2E_BASE_URL and STEPANEL_E2E_PASSWORD for a disposable seeded instance');
  const baseURL = process.env.STEPANEL_E2E_BASE_URL;
  // Keep repeated/parallel Playwright attempts isolated in the shared test
  // control plane rather than racing to create the same customer account.
  const testCustomer = {
    ...customer,
    username: `${customer.username}-w${testInfo.workerIndex}-r${testInfo.repeatEachIndex}`,
  };
  const populatedHosting = hostingFor(testCustomer.username);
  let hosting = { ...populatedHosting, sites: [] };

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
  let backupInventoryRequests = 0;
  let backupChecklistRequests = 0;
  let suspensionRequests = 0;
  let overviewRequests = 0;
  await page.route('**/api/backups*', (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.has('limit')) backupChecklistRequests += 1;
    else backupInventoryRequests += 1;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ backups: customerBackups }) });
  });
  let shopRouteConfigured = false;
  let createdSite = null;
  await page.route('**/api/sites/overview', (route) => {
    overviewRequests += 1;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sites: [
    ...hosting.sites.map((site) => site.site === 'e2e-main'
      ? { site: site.site, routes: [{ site: site.site, domain: 'main.example.org' }], database_count: 1, kind: 'main' }
      : { site: site.site, routes: shopRouteConfigured ? [{ site: site.site, domain: 'shop.example.org' }] : [], database_count: 0, kind: 'addon' }),
    ...(createdSite ? [{ site: createdSite.site, routes: [{ site: createdSite.site, domain: createdSite.domain }], database_count: 1, kind: 'addon' }] : []),
    ] }) });
  });
  await page.route('**/api/sites/suspensions', (route) => {
    suspensionRequests += 1;
    return route.fulfill({ status: 200, contentType: 'application/json', body: '{"suspensions":[]}' });
  });
  const writes = [];
  const writeRevisions = [];
  let accountMfaEnabled = false;
  const testSetupSecret = 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP';
  let confirmedMfaCode = '';
  let confirmedMfaCurrentPassword = '';
  await page.route('**/api/account/me', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
    username: testCustomer.username, tenant_id: testCustomer.username, role: 'owner', plan: 'professional',
    created_at: '2026-01-01T00:00:00Z', sites: [], sites_used: 0, site_limit: 5, sites_percent: 0,
    databases_used: 0, database_limit: 10, databases_percent: 0, warning_threshold_percent: 80,
    critical_threshold_percent: 95, cpu_percent: 200, memory_mb: 1024, disk_mb: 51200,
    inodes: 1000000, tasks_max: 256, redis_memory_mb: 1024,
    mfa_enabled: accountMfaEnabled, mfa_enrollment_required: false,
    password_reset_required: false, suspended: false,
  }) }));
  await page.route('**/api/account/mfa/setup', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
    secret: testSetupSecret,
    otpauth_uri: `otpauth://totp/StePanel:${testCustomer.username}?secret=${testSetupSecret}&issuer=StePanel`,
    qr_data_url: 'data:image/png;base64,iVBORw0KGgo=',
  }) }));
  await page.route('**/api/account/mfa', async (route) => {
    const body = route.request().postDataJSON();
    confirmedMfaCode = body.code;
    confirmedMfaCurrentPassword = body.current_password;
    accountMfaEnabled = true;
    return route.fulfill({ status: 204, body: '' });
  });
  const revisionA = `"sha256-${'a'.repeat(64)}"`;
  const revisionB = `"sha256-${'b'.repeat(64)}"`;
  let conflictOnMainWrite = true;
  let releaseMainRead;
  const mainReadPending = new Promise((resolve) => { releaseMainRead = resolve; });
  let releaseShopList;
  const shopListPending = new Promise((resolve) => { releaseShopList = resolve; });
  let failNextMainList = false;
  let resumableUploadOffset = 0;
  let resumableUploadChunks = 0;
  await page.route('**/api/sites/files/e2e-main**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname.endsWith('/uploads') && request.method() === 'POST') {
      const body = request.postDataJSON();
      resumableUploadOffset = 0;
      resumableUploadChunks = 0;
      return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ id: 'a'.repeat(32), path: body.path, size: body.size, offset: 0, sha256: body.sha256 }) });
    }
    if (url.pathname.includes('/uploads/')) {
      const isComplete = url.pathname.endsWith('/complete');
      if (isComplete) return route.fulfill({ status: 200, contentType: 'application/json', body: '{"ok":true}' });
      if (request.method() === 'GET') return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'a'.repeat(32), path: 'wp-content/large.bin', size: 4 * 1024 * 1024 + 19, offset: resumableUploadOffset, sha256: 'test' }) });
      resumableUploadChunks += 1;
      resumableUploadOffset = Number(request.headers()['upload-offset']) + request.postDataBuffer().length;
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'a'.repeat(32), path: 'wp-content/large.bin', size: 4 * 1024 * 1024 + 19, offset: resumableUploadOffset, sha256: 'test' }) });
    }
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
    if (failNextMainList) {
      failNextMainList = false;
      return route.fulfill({ status: 503, contentType: 'text/plain', body: 'temporary listing failure' });
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

  // Home for a new tenant: show the first WordPress action without scanning
  // site, backup, or administrator-only suspension inventories.
  await expect(page.getByRole('heading', { name: `Welcome back, ${testCustomer.username}` })).toBeVisible();
  await expect(page.locator('#panelHome')).toHaveText(`/home/${testCustomer.username}`);
  await expect(page.locator('#panelOnboardingList')).toContainText('Create your first WordPress site');
  await expect(page.locator('#panelOnboardingProgress')).toContainText('0 of 3 essentials complete');
  await expect(page.locator('#panelPrimaryAction')).toHaveAttribute('href', '#websites');
  expect(overviewRequests).toBe(0);
  expect(backupChecklistRequests).toBe(0);
  expect(backupInventoryRequests).toBe(0);
  expect(suspensionRequests).toBe(0);
  await expect(page.locator('#panelSiteList')).toContainText('You have no websites yet');
  await expect(page.locator('#siteOverview')).toBeEmpty();

  // Once sites are assigned, load their setup evidence on demand from the
  // checklist refresh; the hidden site grid still waits for its own view.
  hosting = populatedHosting;
  await page.locator('#panelOnboardingRefresh').click();
  await expect(page.locator('#panelOnboardingList')).toContainText('Open your website workspace');
  await expect(page.locator('#panelOnboardingList')).toContainText('No integrity-verified backup is recorded for e2e-shop');
  await expect(page.locator('#panelOnboardingList').getByRole('link', { name: 'Review e2e-shop backups' })).toHaveAttribute('href', '#site=e2e-shop&tab=backups');
  await expect(page.locator('#panelOnboardingList')).toContainText('e2e-shop has no configured domain route');
  await expect(page.locator('#panelPrimaryAction')).toHaveAttribute('href', '#site=e2e-shop&tab=domains');
  await expect(page.locator('#panelSiteList')).toContainText('public_html');
  await expect(page.locator('#panelSiteList')).not.toContainText(`/home/${testCustomer.username}/public_html`);
  expect(overviewRequests).toBe(1);
  expect(backupChecklistRequests).toBe(1);
  expect(backupInventoryRequests).toBe(0);

  // Exercise an unavailable inventory and verify that it is not presented as
  // an empty list. The hidden Websites grid has not made a competing request.
  await page.unroute('**/api/sites/overview');
  await page.route('**/api/sites/overview', (route) => route.fulfill({
    status: 503,
    contentType: 'application/json',
    body: JSON.stringify({ error: 'overview unavailable for test' }),
  }));
  await page.locator('#panelOnboardingRefresh').click();
  await expect(page.locator('#panelOnboardingList')).toContainText('Domain routes could not be checked');
  await expect(page.locator('#panelOnboardingProgress')).toContainText('1 of 3 essentials complete');
  await expect(page.locator('#panelPrimaryAction')).toHaveAttribute('href', '#websites');
  await expect(page.locator('#panelSitesStatus')).toContainText('could not be refreshed');

  await page.unroute('**/api/sites/overview');
  await page.route('**/api/sites/overview', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ sites: [
      { site: 'e2e-main', routes: [{ site: 'e2e-main', domain: 'main.example.org' }], database_count: 1, kind: 'main' },
      { site: 'e2e-shop', routes: shopRouteConfigured ? [{ site: 'e2e-shop', domain: 'shop.example.org' }] : [], database_count: 0, kind: 'addon' },
    ] }),
  }));
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
  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Websites' }).click();
  await expect(page.locator('#siteOverview')).toContainText('e2e-main');
  expect(backupInventoryRequests).toBe(1);
  expect(suspensionRequests).toBe(0);
  await expect(page.getByRole('link', { name: 'Customers' })).toHaveCount(0);
  await expect(page.locator('#siteCreateTemplate')).toHaveValue('wordpress');
  await expect(page.locator('#wordpressCreateFields')).toBeVisible();
  await expect(page.locator('#siteCreateForm > .field input').nth(0)).toHaveAttribute('name', 'domain');
  await expect(page.locator('#siteCreateForm > .field input').nth(1)).toHaveAttribute('name', 'site');
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
  await expect(page.locator('#siteCreateAdvanced')).toHaveJSProperty('open', false);
  await expect(page.locator('#siteCreateKind')).toBeHidden();
  await page.locator('#siteCreateAdvanced summary').click();
  await expect(page.locator('#siteCreateKind')).toBeVisible();
  await page.locator('#siteCreateTemplate').selectOption('wordpress');
  await expect(page.locator('#wordpressCreateFields')).toBeVisible();
  await expect(page.locator('#wordpressCreateFields')).toContainText('Before you create this site');
  await expect(page.locator('#wordpressCreateFields')).toContainText('Point the domain’s DNS A record to this server');
  await expect(page.locator('#wordpressCreateFields')).toContainText('ports 80 and 443');
  await expect(page.locator('#wordpressCreateFields')).toContainText('cannot verify public A/AAAA routing or firewall reachability');
  await page.locator('#siteCreateDomain').fill('www.shop.example.org');
  await expect(page.locator('#siteCreateName')).toHaveValue('shop');
  await page.locator('#siteCreateDomain').fill('store.example.org');
  await expect(page.locator('#siteCreateName')).toHaveValue('store');
  await page.locator('#siteCreateDomain').fill('this-site-name-is-longer-than-thirty-two-characters.example.org');
  await expect(page.locator('#siteCreateName')).toHaveValue('');
  await page.locator('#siteCreateDomain').fill('another.example.org');
  await expect(page.locator('#siteCreateName')).toHaveValue('another');
  await page.locator('#siteCreateName').fill('my-store');
  await page.locator('#siteCreateDomain').fill('last-domain.example.org');
  await expect(page.locator('#siteCreateName')).toHaveValue('my-store');
  await page.locator('#siteCreateDomain').fill('new-domain.example.org');
  await expect(page.locator('#siteCreateTitle')).toBeHidden();
  await expect(page.locator('#siteCreateAdminPassword')).toBeHidden();

  // Customer WordPress creation must complete domain ownership verification
  // before the provisioning API is called, avoiding a predictable HTTP 409.
  let claimRequests = 0;
  let verificationRequests = 0;
  let createRequests = 0;
  let creationBody;
  await page.route('**/api/sites/domains/claim', async (route) => {
    claimRequests += 1;
    const body = route.request().postDataJSON();
    await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({
      site: body.site, domain: body.domain, state: 'pending',
      txt_name: `_stepanel.${body.domain}`, txt_value: 'e2e-domain-proof-token',
    }) });
  });
  await page.route('**/api/sites/domains/verify', async (route) => {
    verificationRequests += 1;
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ state: 'verified' }) });
  });
  await page.route('**/api/sites', async (route) => {
    if (route.request().method() !== 'POST') return route.fallback();
    createRequests += 1;
    creationBody = route.request().postDataJSON();
    createdSite = { site: creationBody.site, domain: creationBody.domain };
    await route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify({ job_id: 'customer-wordpress-create' }) });
  });
  await page.route('**/api/jobs/customer-wordpress-create', (route) => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify({
      id: 'customer-wordpress-create', kind: 'site.create', state: 'completed',
      output: { domain: 'newsite.example.org', database: 'newsite', recovery_point: 'backup-1', recovery_verified_at: '2026-11-22T12:00:00Z' },
    }),
  }));
  await page.evaluate(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: async (value) => { window.__copiedText = value; } },
    });
  });
  await page.locator('#siteCreateDomain').fill('newsite.example.org');
  await page.locator('#siteCreateName').fill('newsite');
  await expect(page.locator('#siteCreateTitle')).toBeHidden();
  await page.locator('#siteCreateForm button[type="submit"]').click();
  await expect(page.locator('#siteCreateStatus')).toContainText('Verify control of this domain first');
  expect(createRequests).toBe(0);
  await page.getByRole('button', { name: 'Get DNS verification record' }).click();
  await expect(page.locator('#wordpressCreateFields')).toContainText('_stepanel.newsite.example.org');
  await expect(page.locator('#wordpressCreateFields')).toContainText('e2e-domain-proof-token');
  await page.getByRole('button', { name: 'Copy record name' }).click();
  await expect(page.locator('#wordpressCreateFields')).toContainText('TXT record name copied');
  expect(await page.evaluate(() => window.__copiedText)).toBe('_stepanel.newsite.example.org');
  await page.getByRole('button', { name: 'Copy TXT value' }).click();
  await expect(page.locator('#wordpressCreateFields')).toContainText('TXT verification value copied');
  expect(await page.evaluate(() => window.__copiedText)).toBe('e2e-domain-proof-token');
  await page.getByRole('button', { name: 'Check TXT record' }).click();
  await expect(page.locator('#wordpressCreateFields')).toContainText('Domain ownership verified for newsite.example.org');
  await page.locator('#siteCreateDomain').fill('changed.example.org');
  await expect(page.locator('#wordpressCreateFields')).toContainText('The website name or domain changed. Request and verify a new DNS TXT record.');
  await page.locator('#siteCreateForm button[type="submit"]').click();
  await expect(page.locator('#siteCreateStatus')).toContainText('Verify control of this domain first');
  expect(createRequests).toBe(0);
  await page.locator('#siteCreateDomain').fill('newsite.example.org');
  await page.getByRole('button', { name: 'Get DNS verification record' }).click();
  await expect(page.locator('#wordpressCreateFields')).toContainText('e2e-domain-proof-token');
  await page.getByRole('button', { name: 'Check TXT record' }).click();
  await expect(page.locator('#wordpressCreateFields')).toContainText('Domain ownership verified for newsite.example.org');
  await expect(page.locator('#siteCreateTitle')).toBeVisible();
  await page.locator('#siteCreateTitle').fill('New Site');
  await page.locator('#siteCreateAdminUser').fill('siteadmin');
  await page.locator('#siteCreateAdminEmail').fill('owner@example.org');
  await page.getByRole('button', { name: 'Generate password' }).click();
  const generatedPassword = await page.locator('#siteCreateAdminPassword').inputValue();
  expect(generatedPassword).toHaveLength(24);
  await page.getByRole('button', { name: 'Copy password' }).click();
  await expect(page.locator('#siteCreateStatus')).toContainText('Password copied');
  expect(await page.evaluate(() => window.__copiedText)).toBe(generatedPassword);
  await page.getByRole('button', { name: 'Show', exact: true }).click();
  await expect(page.locator('#siteCreateAdminPassword')).toHaveAttribute('type', 'text');
  await page.getByRole('button', { name: 'Hide', exact: true }).click();
  await page.locator('#siteCreateAdminPassword').fill(generatedPassword);
  await page.locator('#siteCreateAdminPassword').dispatchEvent('input');
  await expect(page.locator('#siteCreateAdminPassword')).toHaveAttribute('type', 'password');
  await page.locator('#siteCreateForm button[type="submit"]').click();
  const createConfirmation = page.getByRole('dialog');
  await expect(createConfirmation).toBeVisible();
  await createConfirmation.getByLabel('Type newsite to confirm').fill('newsite');
  await createConfirmation.getByRole('button', { name: 'Create WordPress site' }).click();
  await expect(page.locator('#siteCreateStatus')).toContainText('newsite is ready at newsite.example.org');
  expect(claimRequests).toBe(2);
  expect(verificationRequests).toBe(2);
  expect(createRequests).toBe(1);
  expect(creationBody).toMatchObject({ site: 'newsite', domain: 'newsite.example.org', template: 'wordpress' });

  await expect.poll(() => pageErrors).toEqual([]);
  await checkA11y(page);

  // Files: browse, open, edit and save.
  failNextMainList = true;
  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Files' }).click();
  await expect(page.locator('#files')).toBeVisible();
  await expect(page.locator('#fileTransferLimits')).toHaveText('Edit files up to 2 MiB. Upload files up to 100 MiB in resumable, checksum-verified chunks; downloads support files up to 40 GiB.');
  const sftpGuide = page.locator('#fileSftpGuide');
  await expect(sftpGuide).toBeVisible();
  await sftpGuide.locator('summary').click();
  await expect(page.locator('#fileSftpUsername')).toHaveText(testCustomer.username);
  await expect(page.locator('#fileSftpRoot')).toHaveText(`/home/${testCustomer.username}/public_html`);
  await expect(page.locator('#fileSftpSettings')).toHaveAttribute('href', '#site=e2e-main&tab=security');
  await expect(page.locator('#fileStatus')).toContainText('temporary listing failure');
  await sftpGuide.locator('summary').click();
  await page.locator('#fileUpload').evaluate((input) => {
    Object.defineProperty(input, 'files', {
      configurable: true,
      value: [{ name: 'large-backup.tar.gz', size: 100 * 1024 * 1024 + 1 }],
    });
    input.dispatchEvent(new Event('change', { bubbles: true }));
    delete input.files;
  });
  await expect(sftpGuide).toHaveJSProperty('open', true);
  await expect(page.locator('#fileStatus')).toContainText('SFTP details are shown below.');
  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Websites' }).click();
  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Files' }).click();
  await expect(page.getByRole('button', { name: 'index.php', exact: true })).toBeVisible();
  await expect(page.locator('#fileRows tr')).toHaveCount(2);
  await expect(page.locator('#fileRows tr').first().locator('td')).toHaveCount(5);
  await expect(page.locator('#fileRows tr').first().locator('td').first()).toContainText('wp-content');
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
  await page.locator('#fileUpload').setInputFiles({ name: 'large.bin', mimeType: 'application/octet-stream', buffer: Buffer.alloc(4 * 1024 * 1024 + 19, 7) });
  await expect(page.locator('#fileStatus')).toContainText('Uploaded 1 file.');
  expect(resumableUploadChunks).toBe(2);
  await page.getByRole('button', { name: 'index.php', exact: true }).click();
  await page.locator('#fileSite').selectOption('e2e-shop');
  await expect(page.locator('#fileSftpRoot')).toHaveText(`/home/${testCustomer.username}/shop.example.org`);
  await expect(page.locator('#fileSftpSettings')).toHaveAttribute('href', '#site=e2e-shop&tab=security');
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
  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Account' }).click();
  await page.locator('#accountMFASetup summary').click();
  await expect(page.getByRole('button', { name: 'Show setup QR code' })).toBeVisible();
  await page.getByRole('button', { name: 'Show setup QR code' }).click();
  await expect(page.locator('#accountMFAQRCode')).toHaveAttribute('src', /^data:image\/png;base64,/);
  await expect(page.locator('#accountTOTPSecretDisplay')).toHaveText(testSetupSecret);
  await page.locator('#accountMFACurrentPassword').fill('a sufficiently long customer password');
  await page.getByLabel('6-digit code from your authenticator').fill('123456');
  await page.getByRole('button', { name: 'Verify and save MFA' }).click();
  await expect(page.locator('#accountMFAStatus')).toContainText('MFA saved');
  expect(confirmedMfaCode).toBe('123456');
  expect(confirmedMfaCurrentPassword).toBe('a sufficiently long customer password');
  await expect(page.locator('#accountMFASetup')).toBeHidden();
  hosting.sites = [];
  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Websites' }).click();
  await page.locator('#siteCreateTemplate').selectOption('php');
  await page.evaluate(() => { location.hash = '#home'; });
  await expect(page.locator('#home')).toBeVisible();
  await page.locator('#panelOnboardingRefresh').click();
  await expect(page.locator('#panelOnboardingList')).toContainText('Create your first WordPress site');
  await expect(page.locator('#panelPrimaryAction')).toHaveText('Create WordPress site');
  await page.locator('#panelPrimaryAction').click();
  await expect(page.locator('#siteCreateTemplate')).toHaveValue('wordpress');
  await expect(page.locator('#wordpressCreateFields')).toBeVisible();
  await context.close();
});
