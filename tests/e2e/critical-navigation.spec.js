// Navigation and accessibility smoke tests. These prove that each critical
// workspace renders for a seeded instance; they do NOT execute the
// underlying workflows (site creation, deployment, restore, cancellation).
// End-to-end workflow coverage belongs in separate journey specs that assert
// job completion, host state, audit events, and state after a restart.
const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;

const enabled = Boolean(process.env.STEPANEL_E2E_BASE_URL);
const username = process.env.STEPANEL_E2E_USERNAME || 'admin';
const password = process.env.STEPANEL_E2E_PASSWORD;
const totp = process.env.STEPANEL_E2E_TOTP;

test.beforeEach(async ({ page }) => {
  test.skip(!enabled || !password, 'Set STEPANEL_E2E_BASE_URL and STEPANEL_E2E_PASSWORD for a disposable seeded instance');
  await page.goto('/login');
  await page.getByLabel('Username').fill(username);
  await page.getByLabel('Password', { exact: true }).fill(password);
  if (totp) await page.getByLabel('Authenticator code').fill(totp);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).not.toHaveURL(/\/login$/);
});

async function checkA11y(page) {
  const result = await new AxeBuilder({ page }).analyze();
  expect(result.violations, result.violations.map((violation) => `${violation.id}: ${violation.help}`).join('\n')).toEqual([]);
}

test('login and overview accessibility', async ({ page }) => {
  await expect(page.getByRole('main')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Download support bundle' })).toBeVisible();
  await checkA11y(page);
});

test('capture seeded administrator overview screenshots', async ({ page }, testInfo) => {
  const directory = process.env.STEPANEL_E2E_SCREENSHOT_DIR;
  test.skip(!directory, 'Screenshot capture is enabled only by the release/evidence runner');
  await expect(page.getByRole('main')).toBeVisible();
  await page.screenshot({ path: `${directory}/admin-overview-desktop-${testInfo.project.name}.png`, fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: `${directory}/admin-overview-mobile-${testInfo.project.name}.png`, fullPage: true });
});

test('keyboard navigation and appearance dialog', async ({ page }) => {
  await page.getByRole('button', { name: /Appearance/ }).click();
  await expect(page.getByRole('dialog', { name: 'Appearance' })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog', { name: 'Appearance' })).toBeHidden();
  await checkA11y(page);
});

test('mobile navigation manages focus and background interaction', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const toggle = page.locator('#navToggle');
  await toggle.click();
  await expect(toggle).toHaveAttribute('aria-label', 'Close navigation');
  await expect(page.locator('#workspaceSidebar')).toHaveClass(/is-open/);
  await expect.poll(() => page.evaluate(() => document.activeElement?.closest('#workspaceSidebar') !== null)).toBe(true);
  await expect(page.locator('main')).toHaveAttribute('inert', '');
  await page.keyboard.press('Escape');
  await expect(toggle).toHaveAttribute('aria-label', 'Open navigation');
  await expect(toggle).toBeFocused();
  await expect(page.locator('main')).not.toHaveAttribute('inert');
});

test('navigation: Sites workspace is reachable', async ({ page }) => {
  await page.getByRole('link', { name: 'Sites', exact: true }).click();
  await expect(page.locator('#sites')).toBeInViewport();
});

test('navigation highlight and topbar title follow the section in view', async ({ page }) => {
  const title = page.locator('#workspaceContextTitle');
  const overviewTitle = await title.textContent();
  await page.locator('#sites').evaluate((node) => node.scrollIntoView({ block: 'start', behavior: 'instant' }));
  await expect(page.getByRole('link', { name: 'Sites', exact: true })).toHaveAttribute('aria-current', 'page');
  await expect(title).toHaveText('Sites');
  await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'instant' }));
  await expect(page.getByRole('link', { name: 'Overview', exact: true })).toHaveAttribute('aria-current', 'page');
  await expect(title).toHaveText(overviewTitle);
});

test('site overview makes backup failures explicit', async ({ page }) => {
  // The disposable UI harness intentionally has no host sites. Provide a
  // minimal inventory fixture so this test can exercise the site workspace
  // and backup-error rendering without requiring privileged host setup.
  await page.route('**/api/sites/overview', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ sites: [{ site: 'e2e-site', routes: [], applications: [], database_count: 0 }] }),
    });
  });
  await page.route('**/api/sites/overview/e2e-site', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ site: 'e2e-site', routes: [], applications: [], database_count: 0 }),
    });
  });
  await page.route('**/api/backups?site=*&limit=500', async (route) => {
    await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'backup service unavailable' }) });
  });
  // The common login fixture has already loaded the overview once. Reload so
  // both routes above participate in the initial site-grid request rather
  // than waiting for a later tab activation.
  await page.reload();
  await page.getByRole('link', { name: 'Sites', exact: true }).click();
  await expect(page.locator('#siteOverviewStatus')).toContainText(/managed site|No managed sites/);
  const manage = page.getByRole('button', { name: /Manage site/ }).first();
  await expect(manage).toBeVisible();
  await manage.click();
  await expect(page.locator('.overview-stat').filter({ hasText: 'Last verified backup' })).toContainText('Unavailable');
  await expect(page.locator('.overview-stat').filter({ hasText: 'Last verified backup' })).toContainText('Backup API unavailable');
});

test('site settings expose measured PHP-FPM demand and quota evidence', async ({ page }) => {
  const site = { site: 'e2e-resource', routes: [], applications: [], database_count: 0 };
  await page.route('**/api/sites/overview', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sites: [site] }) }));
  await page.route('**/api/sites/overview/e2e-resource', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(site) }));
  await page.route('**/api/sites/environment/e2e-resource', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ environment: {} }) }));
  await page.route('**/api/sites/usage/e2e-resource', (route) => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ bytes: 400 * 1024 * 1024, files: 1500, complete: true, quota: { enforced: true, tree_bytes: 400 * 1024 * 1024, estimated_remaining_bytes: 624 * 1024 * 1024, scope_note: 'Account quota is shared by its sites.' } }),
  }));
  await page.route('**/api/sites/resources/e2e-resource', (route) => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({
      configured: true,
      profile: { cpu_percent: 100, cpu_weight: 100, memory_high_mb: 460, memory_mb: 512, io_weight: 100, tasks_max: 128, php_workers: 4, disk_mb: 1024, inodes: 10000, state: 'applied' },
      observed: { state: 'active', MemoryCurrent: String(192 * 1024 * 1024), MemoryMax: String(512 * 1024 * 1024), CPUUsageNSec: '9000000000', TasksCurrent: '12', TasksMax: '128' },
      fpm_status: {
        available: true, collected_at: '2026-10-29T08:00:00Z',
        pools: [{ php_version: '8.3', state: 'available', active_processes: 3, idle_processes: 1, total_processes: 4, max_children: 4, listen_queue: 2, max_listen_queue: 5, max_children_reached: 7, slow_requests: 11,
          worker_memory: { sampled_workers: 2, total_rss_bytes: 156 * 1024 * 1024, mean_rss_bytes: 78 * 1024 * 1024, p95_rss_bytes: 92 * 1024 * 1024, max_rss_bytes: 92 * 1024 * 1024, projected_rss_at_max_children_bytes: 368 * 1024 * 1024, one_additional_worker_bytes: 92 * 1024 * 1024, estimate: 'instantaneous sample', source: '/proc' } }],
        performance_guidance: [{ php_version: '8.3', title: 'Review PHP-FPM capacity under sustained load', state: 'review', evidence: '3 active, 2 queued; P95 worker RSS 92 MiB.', recommendation: 'Investigate slow requests and compare repeated samples with memory headroom.', operator_approval_needed: true }],
      },
    }),
  }));

  await page.reload();
  await page.getByRole('link', { name: 'Sites', exact: true }).click();
  await page.getByRole('button', { name: /Manage site/ }).first().click();
  await page.getByRole('tab', { name: 'Settings' }).click();
  const panel = page.locator('#workspace-panel');
  await expect(panel).toContainText('Filesystem quota headroom');
  await expect(panel).toContainText('up to 624.0 MB estimated bytes remain');
  await expect(panel).toContainText('PHP-FPM measurements');
  await expect(panel).toContainText('current summed RSS 156.0 MB');
  await expect(panel).toContainText('sampled 10/29/2026');
  await expect(panel).toContainText('Request queue now 2 (historical peak 5)');
  await expect(panel).toContainText('slow requests since FPM start 11');
  await expect(panel).toContainText('P95 worker RSS 92 MiB');
  await expect(panel).toContainText('administrator approval and a separate preview');
});

test('navigation: cPanel migration controls render', async ({ page }) => {
  await page.getByRole('link', { name: 'Migrations' }).click();
  await expect(page.locator('#cpmoveForm')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Import a cpmove backup' })).toBeVisible();
});

test('navigation: Job Center opens', async ({ page }) => {
  await page.getByRole('button', { name: /Operations/ }).click();
  await expect(page.getByRole('heading', { name: 'Operations', exact: true })).toBeVisible();
  await expect(page.locator('#jobCenterList')).toBeVisible();
});

test('navigation: Job Center lists operations or empty state', async ({ page }) => {
  await page.getByRole('button', { name: /Operations/ }).click();
  await expect(page.locator('#jobCenter')).toBeVisible();
  await expect(page.locator('#jobCenter')).toContainText(/No operations|Cancel|Complete/);
});

test('navigation: deployment form renders', async ({ page }) => {
  await page.getByRole('link', { name: 'Deployments' }).click();
  await expect(page.locator('#deployForm')).toBeVisible();
});

test('navigation: WordPress restore form renders', async ({ page }) => {
  await page.getByText('WordPress migration').scrollIntoViewIfNeeded();
  await expect(page.locator('#wpressForm')).toBeVisible();
});

test('navigation: managed database controls render', async ({ page }) => {
  await page.getByRole('link', { name: 'Databases' }).click();
  await expect(page.locator('#database')).toBeInViewport();
  await expect(page.locator('#databaseForm')).toBeVisible();
});

test('major administrator workspaces remain accessible after navigation', async ({ page }) => {
  // Keep accessibility coverage on the states operators actually reach, not
  // only the initial overview. These are intentionally navigation-level
  // checks; destructive workflow journeys must additionally assert API/job
  // outcomes against a disposable seeded host.
  const workspaces = [
    ['Sites', '#sites'],
    ['Migrations', '#migrations'],
    ['Deployments', '#deploy'],
    ['Customers', '#accounts'],
    ['Databases', '#database'],
    ['Infrastructure', '#operations'],
    ['Security', '#security'],
    ['Activity / Jobs', '#jobs'],
  ];
  for (const [label, selector] of workspaces) {
    await page.getByRole('link', { name: label, exact: true }).click();
    await expect(page.locator(selector)).toBeInViewport();
    await checkA11y(page);
  }
});

test('customer session hides administrator navigation', async ({ page }) => {
  test.skip(!process.env.STEPANEL_E2E_CUSTOMER_PASSWORD, 'Provide customer credentials for the second-context isolation check');
  const customer = await page.context().browser().newContext({ baseURL: process.env.STEPANEL_E2E_BASE_URL });
  const customerPage = await customer.newPage();
  await customerPage.goto('/login');
  await customerPage.getByLabel('Username').fill(process.env.STEPANEL_E2E_CUSTOMER_USERNAME || 'customer');
  await customerPage.getByLabel('Password', { exact: true }).fill(process.env.STEPANEL_E2E_CUSTOMER_PASSWORD);
  await customerPage.getByRole('button', { name: 'Sign in' }).click();
  await expect(customerPage.getByRole('link', { name: 'Customers' })).toHaveCount(0);
  await expect(customerPage.getByRole('link', { name: 'Security' })).toHaveCount(0);
  await customer.close();
});

test('shared destructive confirmation requires the exact typed value', async ({ page }) => {
  const result = page.evaluate(() => window.StepanelUI.confirmDangerous({
    title: 'Remove demo-member?', message: 'Access is revoked immediately.',
    facts: [['Member', 'demo-member']], confirmText: 'demo-member', actionLabel: 'Remove member',
  }));
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  const action = dialog.getByRole('button', { name: 'Remove member' });
  await expect(action).toBeDisabled();
  await dialog.getByLabel('Type demo-member to confirm').fill('demo-membe');
  await expect(action).toBeDisabled();
  await dialog.getByLabel('Type demo-member to confirm').fill('demo-member');
  await expect(action).toBeEnabled();
  await checkA11y(page);
  await action.click();
  expect(await result).toBe(true);
});

test('site backups tab shows a failing restore rehearsal and what it proves', async ({ page }) => {
  // Fixture: one site whose most recent rehearsal failed after an earlier
  // pass. The UI must lead with the failure, not the older success.
  const site = { site: 'e2e-site', routes: [], applications: [], database_count: 0 };
  await page.route('**/api/sites/overview', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sites: [site] }) }));
  await page.route('**/api/sites/overview/e2e-site', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(site) }));
  await page.route('**/api/backups?site=*', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ backups: [] }) }));
  const now = new Date().toISOString();
  await page.route('**/api/sites/recovery/e2e-site', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      site: 'e2e-site',
      confidence: 'failing',
      summary: 'The most recent restore rehearsal failed; recovery from this site\'s backups is not proven.',
      reasons: ['Rehearsal of nightly-backup failed: extract verified backup: unexpected EOF'],
      last_backup: { name: 'nightly-backup', created_at: now, age_seconds: 60, integrity: 'verified', encrypted: true },
      offsite: { state: 'pending' },
      last_rehearsal: { at: now, backup: 'nightly-backup', outcome: 'failed', trigger: 'scheduled', level: 'archive', level_description: 'Backup signature verified, archive decrypted and extracted, files and database dumps recovered. Database import and application start are not yet rehearsed.', duration_ms: 1200, error: 'unexpected EOF' },
      last_passed_rehearsal: { at: now, backup: 'older-backup', outcome: 'passed', trigger: 'manual', level: 'archive', level_description: 'Backup signature verified, archive decrypted and extracted, files and database dumps recovered. Database import and application start are not yet rehearsed.', duration_ms: 77000 },
      measured_recovery_ms: 77000,
      recovery_point_age_seconds: 60,
      encryption_key: 'healthy',
      history: [],
      rehearsal_interval_hours: 24,
      automatic_rehearsals_enabled: true,
      scheduled_backups: true,
    }),
  }));
  await page.reload();
  await page.getByRole('link', { name: 'Sites', exact: true }).click();
  await page.getByRole('button', { name: /Manage site/ }).first().click();
  await expect(page.locator('.overview-stat').filter({ hasText: /^Recovery/ })).toContainText('Failing');
  await page.getByRole('tab', { name: 'Backups' }).click();
  const panel = page.getByRole('region', { name: 'Recovery status' });
  await expect(panel).toContainText('Failing');
  await expect(panel.getByRole('list', { name: 'Recovery findings' })).toContainText('unexpected EOF');
  await expect(panel).toContainText('1m 17s');
  await expect(panel).toContainText('Not yet uploaded');
  await expect(panel).toContainText('Database import and application start are not yet rehearsed');
  await expect(panel).toContainText('at most every 24h');
  const result = await new AxeBuilder({ page }).include('section.recovery-status').analyze();
  expect(result.violations.map((violation) => `${violation.id}: ${violation.help}`)).toEqual([]);
});
