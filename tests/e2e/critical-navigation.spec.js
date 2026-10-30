// Navigation and accessibility smoke tests. These prove that each critical
// workspace renders for a seeded instance; they do NOT execute the
// underlying workflows (site creation, deployment, restore, cancellation).
// End-to-end workflow coverage belongs in separate journey specs that assert
// job completion, host state, audit events, and state after a restart.
const { test, expect } = require('@playwright/test');
const AxeBuilder = require('@axe-core/playwright').default;
const fs = require('node:fs/promises');

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

test('site guided workflow connects lifecycle checks and practical diagnostics', async ({ page }) => {
  const site = { site: 'e2e-journey', routes: [{ domain: 'journey.example.test', state: 'applied' }, { domain: 'quiet.example.test', state: 'applied' }], applications: [], database_count: 1 };
  let backupScheduleEnabled = true;
  const json = (body) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/sites/overview', (route) => route.fulfill(json({ sites: [site] })));
  await page.route('**/api/sites/overview/e2e-journey', (route) => route.fulfill(json(site)));
  await page.route('**/api/sites/diagnostics/e2e-journey', (route) => route.fulfill(json({
    collected_at: new Date().toISOString(),
    filesystem: { status: 'observed', mode: '0750', owner_uid: 1001, owner_gid: 1001, detail: 'Path metadata only.' },
    host_storage: [{ label: 'site files', status: 'pressure', available_bytes: 1024, total_bytes: 1048576, available_inodes: 4, total_inodes: 100, detail: 'Filesystem headroom is below 10% for bytes or inodes.' }],
    database: { available: true, engine: 'mariadb', databases: ['journey_wp'], detail: 'Inventory access only.' },
    database_connectivity: { status: 'connected', engine: 'mariadb', collected_at: new Date().toISOString(), detail: 'Restricted host query succeeded.' },
    application_database_connectivity: { status: 'failed', collected_at: new Date().toISOString(), detail: 'WP-CLI db check reports database authentication failure; raw output is withheld.' },
    backup_schedule: backupScheduleEnabled
      ? { enabled: true, interval_minutes: 60, last_run: new Date().toISOString(), last_success: null, has_error_detail: true, consecutive_failures: 2 }
      : { enabled: false },
    tls: [
      { domain: 'journey.example.test', route_state: 'applied', status: 'unknown', renewal: 'dead-letter', renewal_job_id: 'certificate-42', renewal_at: new Date().toISOString(), renewal_evidence: 'Latest certificate issuance job certificate-42 for this domain is in dead-letter state.', detail: 'Live external handshake not tested.' },
      { domain: 'quiet.example.test', route_state: 'applied', status: 'unknown', renewal: 'unknown', renewal_evidence: 'Certificate issuance history could not be read from the durable job store.', detail: 'Live external handshake not tested.' },
    ],
  })));
  await page.route('**/api/sites/usage/e2e-journey', (route) => route.fulfill(json({
    bytes: 950, inodes: 100, complete: true,
    quota: { enforced: true, limit_bytes: 1000, tree_bytes: 950, estimated_remaining_bytes: 50, inode_limit: 100, tree_inodes: 100, estimated_remaining_inodes: 0, scope_note: 'Measured site tree; account quota is shared.' },
  })));
  await page.route('**/api/sites/php/e2e-journey', (route) => route.fulfill(json({ configured: true, profile: { version: '8.3', state: 'applied' } })));
  await page.route('**/api/sites/resources/e2e-journey', (route) => route.fulfill(json({ configured: true, fpm_status: { available: true, collected_at: new Date().toISOString(), pools: [{ php_version: '8.3', state: 'available', saturated: true, active_processes: 8, max_children: 8, listen_queue: 3, slow_requests: 2 }] } })));
  await page.route('**/api/sites/recovery/e2e-journey', (route) => route.fulfill(json({ confidence: 'unverified', last_backup: null, last_passed_rehearsal: null, summary: 'No verified recovery point.' })));
  await page.route('**/api/backups?site=e2e-journey&limit=100', (route) => route.fulfill(json({ backups: [] })));
  await page.route('**/api/wordpress/status/e2e-journey', (route) => route.fulfill(json({ installed: true, wp_cli: true })));
  await page.route('**/api/sites/logs/e2e-journey?source=error&lines=40', (route) => route.fulfill(json({ lines: ['PHP Fatal error: test failure in plugin.php'], available: true })));
  await page.route('**/api/sites/logs/e2e-journey?source=php-fpm&lines=40', (route) => route.fulfill(json({ lines: [], available: true })));
  await page.route('**/api/deployments?site=e2e-journey', (route) => route.fulfill(json({ deployments: [] })));
  await page.route('**/api/wordpress/e2e-journey', (route) => route.fulfill(json({ output: 'Success: WordPress is installed.' })));

  await page.reload();
  await page.getByRole('link', { name: 'Sites', exact: true }).click();
  await page.getByRole('button', { name: /Manage site/ }).click();
  const panel = page.locator('#workspace-panel');
  const diagnosis = panel.getByRole('region', { name: 'Site incident assessment' });
  await expect(diagnosis).toContainText('site files is under capacity pressure');
  await expect(diagnosis).toContainText('1.0 KB of 1.0 MB bytes');
  await expect(diagnosis).toContainText('PHP-FPM pool is currently saturated');
  await expect(diagnosis).toContainText('8/8 active, queue 3, 2 slow request(s)');
  await expect(diagnosis).toContainText('Sampled');
  await expect(diagnosis).toContainText('Site tree is near its configured storage allowance');
  await expect(diagnosis).toContainText('950 B of 1000 B (95%)');
  await expect(diagnosis).toContainText('Site tree is at or above its configured inode allowance');
  await expect(diagnosis).toContainText('WordPress database connection failed');
  await expect(diagnosis).toContainText('database authentication failure');
  await expect(diagnosis).toContainText('Application recovery is not proven');
  await expect(diagnosis).toContainText('No verified recovery point.');
  await expect(diagnosis).toContainText('Scheduled backups are failing');
  await expect(diagnosis).toContainText('2 consecutive failure(s)');
  await expect(diagnosis).toContainText('Scheduled backup recovery point is stale');
  await expect(diagnosis).toContainText('has never succeeded');
  await expect(diagnosis).toContainText('Certificate renewal failed for journey.example.test');
  await expect(diagnosis).toContainText('Latest certificate issuance job certificate-42 for this domain is in dead-letter state.');
  await expect(diagnosis).toContainText('certificate-42');
  await expect(diagnosis).toContainText('Certificate renewal history unavailable for quiet.example.test');
  await expect(diagnosis).toContainText('Certificate issuance history could not be read from the durable job store.');
  const incidentFindings = diagnosis.locator('.incident-finding');
  const findingCount = await incidentFindings.count();
  expect(findingCount).toBeGreaterThan(0);
  for (const finding of await incidentFindings.all()) {
    await expect(finding.locator('strong')).not.toBeEmpty();
    await expect(finding.locator('p')).not.toBeEmpty();
    await expect(finding.locator('small')).not.toBeEmpty();
  }
  await page.getByRole('tab', { name: 'Guided workflow' }).click();
  await expect(panel.getByRole('heading', { name: 'Import or create the site' })).toBeVisible();
  await expect(panel).toContainText('origin and exact set of imported cPanel components are not inferred');
  await expect(panel).toContainText('application login and HTTP behavior are not tested');
  await expect(panel).toContainText('TLS not verified');
  await expect(panel).toContainText('Live application check needed');
  await expect(panel).toContainText('No verified backup found');
  await expect(panel).toContainText('PHP Fatal error: test failure in plugin.php');
  await checkA11y(page);
  backupScheduleEnabled = false;
  await page.getByRole('tab', { name: 'Overview' }).click();
  await expect(panel.getByRole('region', { name: 'Site incident assessment' })).toContainText('Backup freshness policy is not configured');
  await expect(panel.getByRole('region', { name: 'Site incident assessment' })).toContainText('No enabled recurring backup schedule supplies a freshness threshold');
  await expect(panel.getByRole('region', { name: 'Site incident assessment' })).toContainText('Set a backup schedule that matches the required recovery point objective');
  await page.getByRole('tab', { name: 'Guided workflow' }).click();
  await panel.getByRole('button', { name: 'Run WordPress status' }).click();
  await expect(panel.locator('#lifecycleWordPressOutput')).toContainText('Success: WordPress is installed.');
  await expect(panel.getByRole('link', { name: 'Open staging and clone actions' })).toHaveAttribute('href', /tab=backups/);
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

test('cPanel preflight exposes compatibility evidence and downloadable report', async ({ page }) => {
  await page.route('**/api/cpmove/inspect', (route) => route.fulfill({ json: {
    upload_id: 'fixture-upload', archive_bytes: 1024, expanded_bytes: 2048, database_count: 1,
    mailbox_count: 1, dns_zone_count: 1, cron_job_count: 0, has_home: true, has_mysql: true,
    components: [{ category: 'website_files', source: 'home', planned_disposition: 'imported' }],
    compatibility: [{ name: 'PHP extensions', status: 'unknown', details: 'Run source scan.' }],
    database_versions: [{ database: 'acct_wp', source_family: 'MariaDB', source_version: '10.11.6', destination_version: '10.11.6', status: 'passed', details: 'Version line matches.' }],
    domain_mappings: [{ source_domain: 'example.test', kind: 'main', source_document_root: '/home/acct/public_html', destination: 'public_html', status: 'planned', details: 'Provisional.' }],
    mailbox_validation: [{ address: 'web@example.test', maildir_layout: 'complete', message_files: 4, bytes: 2048, status: 'valid', details: 'Staged only.' }],
    dns_cutover_checklist: [{ step: 'Mail routing', status: 'required', detail: 'Validate MX and mail delivery.' }],
    source_scan_recommended: false,
  } }));
  await page.getByRole('link', { name: 'Migrations' }).click();
  await page.locator('#cpmoveForm input[name="backup"]').setInputFiles({ name: 'cpmove-acct.tar.gz', mimeType: 'application/gzip', buffer: Buffer.from('archive') });
  await expect(page.locator('#cpmoveStatus')).toContainText('Ready:');
  const preview = page.locator('.migration-preview[aria-label="Migration preview"]');
  await expect(preview).toContainText('example.test');
  await expect(preview).toContainText('MariaDB 10.11.6');
  await expect(preview).toContainText('Mail routing');
  const downloadPromise = page.waitForEvent('download');
  await page.getByRole('link', { name: 'Download evidence (JSON)' }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toMatch(/\.json$/);
  const downloaded = JSON.parse(await fs.readFile(await download.path(), 'utf8'));
  expect(downloaded.domain_mappings[0].source_domain).toBe('example.test');
  const markdownPromise = page.waitForEvent('download');
  await page.getByRole('link', { name: 'Download evidence (Markdown)' }).click();
  const markdown = await markdownPromise;
  expect(await fs.readFile(await markdown.path(), 'utf8')).toContain('DNS cutover checklist');
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
  // Keep this broad pass focused on navigation reliability. Dedicated
  // workspace and lifecycle tests run Axe where it is most useful, without
  // making this eight-page smoke check repeatedly scan the entire DOM.
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
    await page.locator(selector).scrollIntoViewIfNeeded();
    await expect(page.locator(selector)).toBeInViewport();
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
      last_backup: { name: 'nightly-backup', created_at: now, verified_at: now, age_seconds: 60, integrity: 'verified', encrypted: true },
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
  await expect(panel).toContainText('Last successful backup');
  await expect(panel).toContainText('nightly-backup · signed · archive verified · encrypted');
  await expect(panel).toContainText('Can this backup be restored?');
  await expect(panel).toContainText('Not proven — latest restore test failed');
  await expect(panel).toContainText('Proven recovery point');
  await expect(panel).toContainText('older-backup');
  await expect(panel).toContainText('Newest backup nightly-backup is not covered by this proof.');
  await expect(panel).toContainText('Last successful remote restore');
  await expect(panel).toContainText('Not recorded');
  await expect(panel.getByRole('list', { name: 'Recovery findings' })).toContainText('unexpected EOF');
  await expect(panel).toContainText('1m 17s');
  await expect(panel).toContainText('Not yet uploaded');
  await expect(panel).toContainText('Database import and application start are not yet rehearsed');
  await expect(panel).toContainText('at most every 24h');
  const result = await new AxeBuilder({ page }).include('section.recovery-status').analyze();
  expect(result.violations.map((violation) => `${violation.id}: ${violation.help}`)).toEqual([]);
});

test('recovery status identifies a successfully rehearsed point and remote copy limits', async ({ page }) => {
  const now = new Date().toISOString();
  const site = { site: 'e2e-recovered', routes: [], applications: [], database_count: 1 };
  await page.route('**/api/sites/overview', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sites: [site] }) }));
  await page.route('**/api/sites/overview/e2e-recovered', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(site) }));
  await page.route('**/api/backups?site=*', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ backups: [] }) }));
  await page.route('**/api/sites/recovery/e2e-recovered', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      site: site.site,
      confidence: 'verified',
      summary: 'Backup was recovered in an archive rehearsal.',
      reasons: [],
      last_backup: { name: 'latest-good', created_at: now, verified_at: now, age_seconds: 1, integrity: 'verified', encrypted: true },
      offsite: { state: 'verified', uploaded_at: now, restore_verified_at: now },
      last_rehearsal: { at: now, backup: 'latest-good', outcome: 'passed', trigger: 'scheduled', level: 'archive', level_description: 'Files and database dumps were extracted and validated. Database import and application health were not tested.', duration_ms: 42000 },
      last_passed_rehearsal: { at: now, backup: 'latest-good', outcome: 'passed', trigger: 'scheduled', level: 'archive', level_description: 'Files and database dumps were extracted and validated. Database import and application health were not tested.', duration_ms: 42000 },
      measured_recovery_ms: 42000,
      recovery_point_age_seconds: 1,
      encryption_key: 'healthy',
      history: [],
      rehearsal_interval_hours: 24,
      automatic_rehearsals_enabled: true,
      scheduled_backups: true,
    }),
  }));
  await page.reload();
  await page.getByRole('link', { name: 'Sites', exact: true }).click();
  await page.getByRole('button', { name: /Manage site/ }).click();
  await page.getByRole('tab', { name: 'Backups' }).click();
  const panel = page.getByRole('region', { name: 'Recovery status' });
  await expect(panel).toContainText('Yes — archive recovery passed');
  await expect(panel).toContainText('Proven recovery point');
  await expect(panel).toContainText('latest-good');
  await expect(panel).toContainText('Upload confirmed');
  await expect(panel).toContainText('remote access is not being probed live');
  await expect(panel).toContainText('Last successful remote restore');
  await expect(panel).toContainText('does not by itself prove application health');
});
