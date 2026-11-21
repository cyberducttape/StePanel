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

test('first-run primary action follows the next unresolved setup step', async ({ page }) => {
  let hostNeedsAttention = true;
  let currentSites = [];
  let currentBackups = [];
  await page.route('**/api/sites/overview', (route) => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify({ sites: currentSites }),
  }));
  await page.route('**/api/backups?limit=100', (route) => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify({ backups: currentBackups }),
  }));
  await page.route('**/api/health/operational', (route) => route.fulfill({
    status: hostNeedsAttention ? 503 : 200,
    contentType: 'application/json',
    body: JSON.stringify({
      operational: !hostNeedsAttention,
      checks: { broker: { ready: !hostNeedsAttention, detail: hostNeedsAttention ? 'broker is unavailable' : 'broker is ready' } },
    }),
  }));

  await page.reload();
  const primary = page.locator('#primarySetupAction');
  await expect(primary).toHaveAttribute('href', '#controlPlaneDetailsToggle');
  await expect(primary).toHaveText('Show what needs attention');
  await primary.click();
  await expect(page.locator('#controlPlaneDetails')).toBeVisible();

  hostNeedsAttention = false;
  await page.locator('#refreshOnboarding').click();
  await expect(primary).toHaveAttribute('href', '#sites');
  await expect(primary).toHaveText('Set up a site');
  await expect(page.locator('#onboardingChecklist')).toContainText('Create or migrate your first site');

  currentSites = [{ site: 'setup-a' }, { site: 'setup-b' }];
  currentBackups = [{ site: 'setup-a', verified_at: '2026-11-20T12:00:00Z' }];
  await page.locator('#refreshOnboarding').click();
  await expect(page.locator('#onboardingChecklist')).toContainText('No integrity-verified backup is recorded for setup-b');
  await expect(page.locator('#onboardingChecklist').getByRole('link', { name: 'Review setup-b backups' })).toHaveAttribute('href', '#site=setup-b&tab=backups');
  await expect(primary).toHaveAttribute('href', '#site=setup-b&tab=backups');

  currentBackups = [
    { site: 'setup-a', verified_at: '2026-11-20T12:00:00Z' },
    ...Array.from({ length: 99 }, (_, index) => ({ site: `historical-${index}`, verified_at: '2026-11-19T12:00:00Z' })),
  ];
  await page.locator('#refreshOnboarding').click();
  const recoveryStep = page.locator('#onboardingChecklist .setup-step').filter({ hasText: 'Create a verified recovery point' });
  await expect(recoveryStep).toHaveClass(/setup-step-unknown/);
  await expect(recoveryStep).toContainText('latest 100 results');
  await expect(primary).toHaveAttribute('href', '#site=setup-b&tab=backups');

  currentBackups = [
    { site: 'setup-a', verified_at: '2026-11-20T12:00:00Z' },
    { site: 'setup-b', verified_at: '2026-11-20T13:00:00Z' },
  ];
  await page.locator('#refreshOnboarding').click();
  await expect(recoveryStep).toHaveClass(/setup-step-done/);
  await expect(recoveryStep).toContainText('Every active site has an integrity-verified backup');
});

test('database health expands into a readable, host-wide snapshot', async ({ page }) => {
  let requests = 0;
  await page.route('**/api/database/diagnostics', async (route) => {
    requests += 1;
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        available: true,
        engine: 'mariadb',
        collected_at: '2026-11-21T18:00:00Z',
        values: { connections: 31, active_connections: 4, blocked_sessions: 2, long_transactions: 1, database_bytes: 4096 },
        warnings: ['blocked database sessions require investigation'],
      }),
    });
  });

  const disclosure = page.locator('#databaseDiagnostics');
  await expect(disclosure).toBeVisible();
  await expect(disclosure).toContainText('Open this section to run a short, read-only health check.');
  await disclosure.locator('summary').click();
  await expect(disclosure).toContainText('31');
  await expect(disclosure).toContainText('4');
  await expect(disclosure).toContainText('2 blocked database session(s)');
  await expect(disclosure).toContainText('host-wide evidence');
  await expect(disclosure.locator('a[href^="/api/"]')).toHaveCount(0);
  expect(requests).toBe(1);
  await disclosure.getByRole('button', { name: 'Refresh health check' }).click();
  await expect.poll(() => requests).toBe(2);
});

test('database health distinguishes unavailable evidence and can retry', async ({ page }) => {
  let requests = 0;
  await page.route('**/api/database/diagnostics', async (route) => {
    requests += 1;
    const unavailable = requests === 1;
    await route.fulfill({
      status: unavailable ? 503 : 200,
      contentType: 'application/json',
      body: JSON.stringify(unavailable ? {
        available: false,
        engine: 'mariadb',
        failure_code: 'socket_unavailable',
        detail: 'The database socket could not be opened. Verify the service and configured socket path.',
      } : {
        available: true,
        engine: 'mariadb',
        values: { connections: 12 },
        warnings: [],
      }),
    });
  });

  const disclosure = page.locator('#databaseDiagnostics');
  await disclosure.locator('summary').click();
  await expect(disclosure).toContainText('The database socket could not be opened. Verify the service and configured socket path.');
  await expect(disclosure.locator('#databaseDiagnosticsStatus')).toHaveAttribute('data-state', 'unknown');
  await disclosure.getByRole('button', { name: 'Refresh health check' }).click();
  await expect(disclosure).toContainText('12');
  await expect(disclosure.locator('#databaseDiagnosticsStatus')).toHaveAttribute('data-state', 'ready');
  expect(requests).toBe(2);
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
  const accessAt = new Date();
  const accessTimestamp = `[${String(accessAt.getUTCDate()).padStart(2, '0')}/${accessAt.toLocaleString('en-US', { month: 'short', timeZone: 'UTC' })}/${accessAt.getUTCFullYear()}:${accessAt.toISOString().slice(11, 19)} +0000]`;
  let backupScheduleEnabled = true;
  let backupHasRun = false;
  const json = (body) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/sites/overview', (route) => route.fulfill(json({ sites: [site] })));
  await page.route('**/api/sites/overview/e2e-journey', (route) => route.fulfill(json(site)));
  await page.route('**/api/sites/diagnostics/e2e-journey', (route) => route.fulfill(json({
    collected_at: new Date().toISOString(),
    filesystem: { status: 'observed', mode: '0750', owner_uid: 1001, owner_gid: 1001, detail: 'Path metadata only.' },
    host_storage: [{ label: 'site files', status: 'pressure', pressure_reasons: ['free bytes below 10%', 'free inodes below 10%'], available_bytes: 1024, total_bytes: 1048576, available_inodes: 4, total_inodes: 100, detail: 'Filesystem headroom is below 10% for: free bytes below 10%, free inodes below 10%.' }],
    database: { available: true, engine: 'mariadb', databases: ['journey_wp'], detail: 'Inventory access only.' },
    database_connectivity: { status: 'unavailable', engine: 'mariadb', failure_code: 'connection_refused', collected_at: new Date().toISOString(), detail: 'The database helper reported that the local database connection was refused. Check that the configured database service is running.' },
    application_database_connectivity: { status: 'failed', collected_at: new Date().toISOString(), detail: 'WP-CLI db check reports database authentication failure; raw output is withheld.' },
    backup_schedule: backupScheduleEnabled
      ? backupHasRun
        ? { enabled: true, interval_minutes: 60, last_run: new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString(), next_run: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(), last_success: null, has_error_detail: true, consecutive_failures: 2 }
        : { enabled: true, interval_minutes: 60, next_run: new Date(Date.now() + 60 * 60 * 1000).toISOString(), last_success: null, has_error_detail: false, consecutive_failures: 0 }
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
  await page.route('**/api/sites/resources/e2e-journey', (route) => route.fulfill(json({ configured: true, observed: { state: 'active', ActiveState: 'active', SubState: 'running', Result: 'success', OOMEvents: '3', OOMKillEvents: '2', OOMEvidence: 'available' }, fpm_status: { available: true, collected_at: new Date().toISOString(), pools: [{ php_version: '8.3', state: 'available', saturated: true, active_processes: 8, max_children: 8, listen_queue: 3, slow_requests: 2 }] } })));
  await page.route('**/api/sites/recovery/e2e-journey', (route) => route.fulfill(json({ confidence: 'unverified', last_backup: null, last_passed_rehearsal: null, summary: 'No verified recovery point.' })));
  await page.route('**/api/backups?site=e2e-journey&limit=100', (route) => route.fulfill(json({ backups: [] })));
  await page.route('**/api/wordpress/status/e2e-journey', (route) => route.fulfill(json({ installed: true, wp_cli: true })));
  await page.route('**/api/sites/logs/e2e-journey?source=error&lines=40', (route) => route.fulfill(json({ lines: ['PHP Fatal error: test failure in plugin.php'], available: true })));
  await page.route('**/api/sites/logs/e2e-journey?source=php-fpm&lines=40', (route) => route.fulfill(json({ lines: [], available: true })));
  await page.route('**/api/sites/logs/e2e-journey?source=access&lines=100', (route) => route.fulfill(json({ lines: [`198.51.100.7 - - ${accessTimestamp} "GET / HTTP/1.1" 503 211`], available: true })));
  await page.route('**/api/sites/logs/e2e-journey?source=error&lines=100', (route) => route.fulfill(json({ lines: ['upstream connect() failed while connecting to PHP-FPM'], available: true, collected_at: new Date().toISOString() })));
  await page.route('**/api/deployments?site=e2e-journey', (route) => route.fulfill(json({ deployments: [{ stage: 'activation', state: 'completed', created_at: accessAt.toISOString() }] })));
  await page.route('**/api/wordpress/e2e-journey', (route) => route.fulfill(json({ output: 'Success: WordPress is installed.' })));

  await page.reload();
  await page.getByRole('link', { name: 'Sites', exact: true }).click();
  await page.getByRole('button', { name: /Manage site/ }).click();
  const panel = page.locator('#workspace-panel');
  const diagnosis = panel.getByRole('region', { name: 'Site incident assessment' });
  await expect(diagnosis).not.toContainText('Scheduled backup recovery point is stale');
  await expect(diagnosis).toContainText('site files is under capacity pressure');
  await expect(diagnosis).toContainText('free bytes below 10% and free inodes below 10%');
  await expect(diagnosis).toContainText('1.0 KB of 1.0 MB bytes');
  await expect(diagnosis).toContainText('PHP-FPM pool is currently saturated');
  await expect(diagnosis.getByRole('region', { name: 'HTTP 503 diagnosis' })).toContainText('1 HTTP 503 response(s) observed');
  await expect(diagnosis.getByRole('region', { name: 'HTTP 503 diagnosis' })).toContainText('8/8 workers active, queue 3');
  await expect(diagnosis.getByRole('region', { name: 'HTTP 503 diagnosis' })).toContainText('2 cgroup OOM kill(s) recorded since this cgroup was created');
  await expect(diagnosis.getByRole('region', { name: 'HTTP 503 diagnosis' })).toContainText('Deployment history');
  await expect(diagnosis.getByRole('region', { name: 'HTTP 503 diagnosis' })).toContainText('Timing is correlation evidence');
  await expect(diagnosis).toContainText('8/8 active, queue 3, 2 slow request(s)');
  await expect(diagnosis).toContainText('Sampled');
  await expect(diagnosis).toContainText('Site tree is near its configured storage allowance');
  await expect(diagnosis).toContainText('950 B of 1000 B (95%)');
  await expect(diagnosis).toContainText('Site tree is at or above its configured inode allowance');
  await expect(diagnosis).toContainText('WordPress database connection failed');
  await expect(diagnosis).toContainText('database authentication failure');
  await expect(diagnosis).toContainText('Database engine connectivity probe failed');
  await expect(diagnosis).toContainText('connection_refused');
  await expect(diagnosis).toContainText('connection was refused');
  await expect(diagnosis).toContainText('Application recovery is not proven');
  await expect(diagnosis).toContainText('No verified recovery point.');
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
  await panel.getByRole('button', { name: 'Run WordPress status' }).click();
  await expect(panel.locator('#lifecycleWordPressOutput')).toContainText('Success: WordPress is installed.');
  await expect(panel.getByRole('link', { name: 'Open staging and clone actions' })).toHaveAttribute('href', /tab=backups/);
  backupHasRun = true;
  await page.reload();
  await page.getByRole('link', { name: 'Sites', exact: true }).click();
  await page.getByRole('button', { name: /Manage site/ }).click();
  const staleDiagnosis = page.locator('#workspace-panel').getByRole('region', { name: 'Site incident assessment' });
  await expect(staleDiagnosis).toContainText('Scheduled backups are failing');
  await expect(staleDiagnosis).toContainText('2 consecutive failure(s)');
  await expect(staleDiagnosis).toContainText('Scheduled backup recovery point is stale');
  await expect(staleDiagnosis).toContainText('has never succeeded');
  await expect(staleDiagnosis).toContainText('last attempt');
  backupScheduleEnabled = false;
  await page.getByRole('tab', { name: 'Overview' }).click();
  const unscheduledDiagnosis = page.locator('#workspace-panel').getByRole('region', { name: 'Site incident assessment' });
  await expect(unscheduledDiagnosis).toContainText('Backup freshness policy is not configured');
  await expect(unscheduledDiagnosis).toContainText('No enabled recurring backup schedule supplies a freshness threshold');
  await expect(unscheduledDiagnosis).toContainText('Set a backup schedule that matches the required recovery point objective');
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

// Every site workspace tab, including the forms the tabs build in
// JavaScript, passes axe in two states. With only the site inventory
// stubbed, each tab's own requests reach the disposable instance, which
// knows no such site, so tabs render their unavailable states. With the
// per-site inventories stubbed empty, list tabs render their empty states.
async function scanSiteWorkspaceTabs(page, siteName) {
  await page.reload();
  await page.getByRole('link', { name: 'Sites', exact: true }).click();
  await page.getByRole('button', { name: /Manage site/ }).first().click();
  const tabs = page.getByRole('tablist', { name: `${siteName} sections` }).getByRole('tab');
  const panel = page.locator('#workspace-panel');
  const count = await tabs.count();
  expect(count).toBeGreaterThanOrEqual(12);
  for (let index = 0; index < count; index += 1) {
    const tab = tabs.nth(index);
    const label = (await tab.textContent()).trim();
    await tab.click();
    await expect(tab).toHaveAttribute('aria-selected', 'true');
    await expect(panel).toHaveAttribute('aria-labelledby', await tab.getAttribute('id'));
    await expect(panel.locator(':scope > p.import-note', { hasText: 'Loading…' })).toHaveCount(0);
    const result = await new AxeBuilder({ page }).include('#siteDetail').analyze();
    expect(result.violations, `${label} tab:\n` + result.violations.map((violation) => `${violation.id}: ${violation.help} (${violation.nodes.map((node) => node.target.join(' ')).join(', ')})`).join('\n')).toEqual([]);
  }
}

function stubSiteInventory(page, site) {
  const json = (body) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  return Promise.all([
    page.route('**/api/sites/overview', (route) => route.fulfill(json({ sites: [site] }))),
    page.route(`**/api/sites/overview/${site.site}`, (route) => route.fulfill(json(site))),
  ]);
}

test('every site workspace tab passes axe when its data is unavailable', async ({ page }) => {
  const site = { site: 'e2e-a11y', routes: [{ domain: 'a11y.example.test', state: 'applied' }], applications: [], database_count: 0 };
  await stubSiteInventory(page, site);
  await scanSiteWorkspaceTabs(page, site.site);
});

test('every site workspace tab passes axe with empty inventories', async ({ page }) => {
  const site = { site: 'e2e-empty', routes: [], applications: [], database_count: 0 };
  const json = (body) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  await stubSiteInventory(page, site);
  await page.route('**/api/backups?site=e2e-empty*', (route) => route.fulfill(json({ backups: [] })));
  await page.route('**/api/databases', (route) => route.fulfill(json({ databases: [] })));
  await page.route('**/api/deployments?site=e2e-empty', (route) => route.fulfill(json({ deployments: [] })));
  await page.route('**/api/workers/e2e-empty', (route) => route.fulfill(json({ workers: [] })));
  await page.route('**/api/tasks/e2e-empty', (route) => route.fulfill(json({ tasks: [] })));
  await page.route('**/api/sites/access/e2e-empty', (route) => route.fulfill(json({ sftp_enabled: false, shell_enabled: false, keys: [] })));
  await scanSiteWorkspaceTabs(page, site.site);
  // The empty states are list items, so assistive technology announces the
  // list and its single explanatory entry.
  await page.getByRole('tab', { name: 'Backups' }).click();
  await expect(page.locator('#workspace-panel ul.resource-list > li.empty-state')).toHaveText('No backups yet. Create one above.');
});

test('navigation: cPanel migration controls render', async ({ page }) => {
  await page.getByRole('link', { name: 'Migrations' }).click();
  await expect(page.locator('#cpmoveForm')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Import a cpmove backup' })).toBeVisible();
});

test('high-risk change confirmation exposes the complete review plan', async ({ page }) => {
  await page.evaluate(() => {
    const opener = document.createElement('button');
    opener.id = 'reviewDialogOpener';
    opener.textContent = 'Open review';
    document.querySelector('main').append(opener);
    opener.focus();
    window.__reviewDialog = window.StepanelUI.confirmDangerous({
      title: 'Review risky change', message: 'Confirm only after reviewing the plan.',
      confirmText: 'example.test', plan: {
        before: { php: '8.2', memory_limit: '256M' },
        proposed: { php: '8.3', memory_limit: '512M' },
        validation_commands: [{ when: 'before', command: 'php-fpm -t', expected: 'Syntax is valid.', automated: true }],
        expected_interruptions: [{ point: 'During apply', expected: 'Desired state stays pending.', recovery: 'Reconcile the PHP-FPM pool.' }],
        desired_state_proof: ['Profile is persisted as applied after helper success.'],
        recovery_plan: ['Restore the previous profile.'],
      },
    });
  });
  const dialog = page.locator('dialog.confirm-dialog');
  // The typed confirmation takes focus when the dialog opens.
  await expect(page.getByLabel('Type example.test to confirm')).toBeFocused();
  await expect(dialog).toContainText('Exact before/after configuration');
  await dialog.locator('summary').click();
  await expect(dialog).toContainText('"memory_limit": "256M"');
  await expect(dialog).toContainText('"memory_limit": "512M"');
  await expect(dialog).toContainText('php-fpm -t');
  await expect(dialog).toContainText('During apply');
  await expect(dialog).toContainText('Restore the previous profile.');
  // The dialog is announced by its title and message, passes axe while
  // open, and returns focus to what opened it when cancelled.
  await expect(page.getByRole('dialog', { name: 'Review risky change' })).toBeVisible();
  await expect(dialog).toHaveAccessibleDescription('Confirm only after reviewing the plan.');
  const result = await new AxeBuilder({ page }).include('dialog.confirm-dialog').analyze();
  expect(result.violations, result.violations.map((violation) => `${violation.id}: ${violation.help}`).join('\n')).toEqual([]);
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toHaveCount(0);
  expect(await page.evaluate(() => window.__reviewDialog)).toBe(false);
  await expect(page.locator('#reviewDialogOpener')).toBeFocused();
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
    dns_comparisons: [{ zone: 'example.test', source_status: 'archive_zone_found', destination_status: 'not_compared', details: 'Review the authoritative destination zone.' }],
    tls_certificates: [{ domains: ['example.test', 'www.example.test'], status: 'valid', destination_status: 'not_imported', fingerprint_sha256: 'abc123', details: 'Public metadata only.' }],
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
  await expect(preview).toContainText('DNS source and destination comparison');
  await expect(preview).toContainText('TLS certificate differences');
  const downloadPromise = page.waitForEvent('download');
  await page.getByRole('link', { name: 'Download evidence (JSON)' }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toMatch(/\.json$/);
  const downloaded = JSON.parse(await fs.readFile(await download.path(), 'utf8'));
  expect(downloaded.domain_mappings[0].source_domain).toBe('example.test');
  expect(downloaded.dns_comparisons[0].destination_status).toBe('not_compared');
  expect(downloaded.tls_certificates[0].fingerprint_sha256).toBe('abc123');
  const markdownPromise = page.waitForEvent('download');
  await page.getByRole('link', { name: 'Download evidence (Markdown)' }).click();
  const markdown = await markdownPromise;
  expect(await fs.readFile(await markdown.path(), 'utf8')).toContain('DNS cutover checklist');
});

test('navigation: Job Center opens', async ({ page }) => {
  await page.getByRole('button', { name: /Open operations/ }).click();
  await expect(page.getByRole('heading', { name: 'Operations', exact: true })).toBeVisible();
  await expect(page.locator('#jobCenterList')).toBeVisible();
  await expect(page.getByRole('link', { name: 'View JSON' })).toHaveCount(0);
  await expect(page.locator('#jobFeed a[href^="/api/jobs/"]')).toHaveCount(0);
});

test('navigation: Job Center lists operations or empty state', async ({ page }) => {
  await page.getByRole('button', { name: /Operations/ }).click();
  await expect(page.locator('#jobCenter')).toBeVisible();
  await expect(page.locator('#jobCenter')).toContainText(/No operations|Cancel|Complete/);
});

test('operation history shows readable details instead of raw JSON', async ({ page }) => {
  const operation = {
    id: 'job-ux-example', kind: 'site.backup', user: 'example', state: 'failed',
    started_at: '2026-11-20T12:00:00Z', finished_at: '2026-11-20T12:01:00Z',
    attempts: 2, max_attempts: 3, error: 'Database connection timed out',
  };
  await page.route('**/api/jobs', (route) => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify({ jobs: [operation] }),
  }));
  await page.evaluate(() => window.StepanelJobs.load());
  await page.getByRole('button', { name: /Operations/ }).click();

  const row = page.locator('#jobCenterList [data-job-id="job-ux-example"]');
  await expect(row).toContainText('Backup · example');
  await row.getByText('Operation details').click();
  await expect(row).toContainText('Job ID');
  await expect(row).toContainText('job-ux-example');
  await expect(row).toContainText('2 of 3');
  await expect(row).toContainText('Database connection timed out');
  await expect(row.locator('a[href^="/api/jobs/"]')).toHaveCount(0);
  await row.getByText('Operation details').click();
  await page.locator('#jobFeed button.activity-link').click();
  await expect(row.locator('details')).toHaveAttribute('open', '');
  await checkA11y(page);
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

test('recovery status shows evidence level, successful test, measured recovery, and offsite restore', async ({ page }) => {
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
      confidence_level: 'synthetic_transaction_passed',
      summary: 'The backup passed database, application, and synthetic transaction checks.',
      reasons: [],
      last_backup: { name: 'latest-good', created_at: now, verified_at: now, age_seconds: 1, integrity: 'verified', encrypted: true },
      offsite: { state: 'verified', uploaded_at: now, restore_verified_at: now },
      last_rehearsal: { at: now, backup: 'latest-good', outcome: 'passed', trigger: 'scheduled', level: 'application', synthetic_transaction_passed: true, level_description: 'Database and application recovered and passed a synthetic transaction.', duration_ms: 42000 },
      last_passed_rehearsal: { at: now, backup: 'latest-good', outcome: 'passed', trigger: 'scheduled', level: 'application', synthetic_transaction_passed: true, level_description: 'Database and application recovered and passed a synthetic transaction.', duration_ms: 42000 },
      measured_recovery_ms: 42000,
      recovery_point_age_seconds: 1,
      encryption_key: 'healthy',
      checks: [
        { name: 'archive', status: 'pass', detail: 'Manifest and archive verified.', at: now, backup: 'latest-good' },
        { name: 'database', status: 'pass', detail: 'Database imported and checked.', at: now, backup: 'latest-good', duration_ms: 42000 },
        { name: 'application', status: 'pass', detail: 'Application booted and HTTP probe passed.', at: now, backup: 'latest-good', duration_ms: 42000 },
        { name: 'synthetic_transaction', status: 'pass', detail: 'Unique canary write/read passed.', at: now, backup: 'latest-good', duration_ms: 42000 },
        { name: 'offsite_restore', status: 'pass', detail: 'Offsite copy restored.', at: now, backup: 'latest-good' },
      ],
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
  await expect(panel).toContainText('Yes — application recovery passed');
  await expect(panel).toContainText('Synthetic transaction passed');
  await expect(panel).toContainText('Last successful test');
  await expect(panel).toContainText('42s');
  await expect(panel.getByRole('list', { name: 'Recovery evidence steps' })).toContainText('synthetic transaction');
  await expect(panel.getByRole('list', { name: 'Recovery evidence steps' })).toContainText('offsite restore');
  await expect(panel).toContainText('Proven recovery point');
  await expect(panel).toContainText('latest-good');
  await expect(panel).toContainText('Upload confirmed');
  await expect(panel).toContainText('remote access is not being probed live');
  await expect(panel).toContainText('Last successful remote restore');
  await expect(panel).toContainText('does not by itself prove application health');
});
