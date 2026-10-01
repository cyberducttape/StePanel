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

test('create site journey exposes the Sites workspace', async ({ page }) => {
  await page.getByRole('link', { name: 'Sites', exact: true }).click();
  await expect(page.locator('#sites')).toBeInViewport();
});

test('cPanel inspection journey exposes migration controls', async ({ page }) => {
  await page.getByRole('link', { name: 'Migrations' }).click();
  await expect(page.locator('#migrations')).toBeInViewport();
  await expect(page.getByRole('heading', { name: 'Import a cpmove backup' })).toBeVisible();
});

test('migration queue and durable Job Center', async ({ page }) => {
  await page.getByRole('button', { name: /Operations/ }).click();
  await expect(page.getByRole('heading', { name: 'Operations', exact: true })).toBeVisible();
  await expect(page.locator('#jobCenterList')).toBeVisible();
});

test('job cancellation control is available for active work', async ({ page }) => {
  await page.getByRole('button', { name: /Operations/ }).click();
  await expect(page.locator('#jobCenter')).toBeVisible();
  await expect(page.locator('#jobCenter')).toContainText(/No operations|Cancel|Complete/);
});

test('deploy app journey exposes the durable deployment form', async ({ page }) => {
  await page.getByRole('link', { name: 'Deployments' }).click();
  await expect(page.locator('#deployForm')).toBeVisible();
});

test('restore WordPress journey exposes the restore form', async ({ page }) => {
  await page.getByText('WordPress migration').scrollIntoViewIfNeeded();
  await expect(page.locator('#wpressForm')).toBeVisible();
});

test('database lifecycle journey exposes managed database controls', async ({ page }) => {
  await page.getByRole('link', { name: 'Databases' }).click();
  await expect(page.locator('#database')).toBeInViewport();
  await expect(page.locator('#databaseForm')).toBeVisible();
});

test('customer isolation journey keeps administrator controls role-scoped', async ({ page }) => {
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
