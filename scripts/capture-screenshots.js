// Captures the README and docs/SCREENSHOTS.md images from a running,
// disposable StePanel instance. scripts/capture-screenshots.sh starts that
// instance; this script seeds synthetic data through the same API the
// dashboard uses and then photographs the real UI. Nothing here is mocked
// in the browser: every value on screen comes from the running server.
//
// Environment: STEPANEL_SHOT_BASE_URL, STEPANEL_SHOT_DIR,
// STEPANEL_SHOT_ADMIN_USER, STEPANEL_SHOT_ADMIN_PASSWORD, and optionally
// PLAYWRIGHT_CHANNEL (for example "chrome").
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('@playwright/test');

const base = process.env.STEPANEL_SHOT_BASE_URL;
const outDir = process.env.STEPANEL_SHOT_DIR;
const admin = { username: process.env.STEPANEL_SHOT_ADMIN_USER, password: process.env.STEPANEL_SHOT_ADMIN_PASSWORD };
if (!base || !outDir || !admin.username || !admin.password) {
  console.error('STEPANEL_SHOT_BASE_URL, STEPANEL_SHOT_DIR, STEPANEL_SHOT_ADMIN_USER and STEPANEL_SHOT_ADMIN_PASSWORD are required');
  process.exit(64);
}

// Synthetic demonstration data. Domains use the reserved .example TLD.
const sites = ['northwind', 'acme-shop', 'field-notes'];
const routes = [['northwind', 'northwind.example'], ['northwind', 'www.northwind.example'], ['acme-shop', 'shop.acme.example'], ['field-notes', 'notes.example']];
const customer = { username: 'northwind-team', password: 'Northwind-Demo-Password-2026!', totp: 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP', plan: 'professional', sites: ['northwind'] };
const desktop = { width: 1440, height: 900 };
const mobile = { width: 390, height: 844 };
const retroNeonPreferences = { theme: 'retro-neon', dark: true, scale: 100 };

function base32Decode(text) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = '';
  for (const ch of text.replace(/=+$/, '')) bits += alphabet.indexOf(ch).toString(2).padStart(5, '0');
  const bytes = [];
  for (let i = 0; i + 8 <= bits.length; i += 8) bytes.push(parseInt(bits.slice(i, i + 8), 2));
  return Buffer.from(bytes);
}

// RFC 6238 code for the synthetic customer's TOTP seed.
function totp(secret) {
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const digest = crypto.createHmac('sha1', base32Decode(secret)).update(counter).digest();
  const offset = digest[digest.length - 1] & 0xf;
  return String((digest.readUInt32BE(offset) & 0x7fffffff) % 1000000).padStart(6, '0');
}

async function signIn(page, { username, password, code }) {
  await page.goto(base + '/login');
  await page.getByLabel('Username').fill(username);
  await page.getByLabel('Password', { exact: true }).fill(password);
  if (code) await page.getByLabel('Authenticator code').fill(code);
  // Use the context request client so Playwright adopts the same session
  // cookies as the browser page, while avoiding inconsistent native-form
  // activation in headless browser environments.
  const form = { username, password };
  if (code) form.totp = code;
  const response = await page.request.post(base + '/login', { form, maxRedirects: 0 });
  if (response.status() !== 303) throw new Error(`login failed: HTTP ${response.status()}`);
  await page.goto(base + '/');
}

// call sends an API request from the signed-in page with the dashboard's
// CSRF header, exactly as web/static/api.js does.
function call(page, method, url, body) {
  return page.evaluate(async ([method, url, body]) => {
    const csrf = (document.cookie.match(/(?:^|; )stepanel_csrf=([^;]+)/) || [])[1] || '';
    const response = await fetch(url, { method, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': decodeURIComponent(csrf) }, body: body ? JSON.stringify(body) : undefined });
    return { status: response.status, body: await response.text() };
  }, [method, url, body]);
}

async function expectStatus(result, allowed, what) {
  if (!allowed.includes(result.status)) throw new Error(`${what}: HTTP ${result.status}: ${result.body}`);
  return result.body ? JSON.parse(result.body) : {};
}

async function waitForJob(page, id) {
  for (let i = 0; i < 120; i++) {
    const job = await expectStatus(await call(page, 'GET', '/api/jobs/' + encodeURIComponent(id)), [200], 'job ' + id);
    if (job.state === 'completed') return job;
    if (job.state === 'failed' || job.state === 'dead') throw new Error(`job ${id} ${job.state}: ${JSON.stringify(job)}`);
    await page.waitForTimeout(250);
  }
  throw new Error(`job ${id} did not finish`);
}

async function seed(page) {
  for (const site of sites) {
    const job = await expectStatus(await call(page, 'POST', '/api/sites', { site, template: 'php' }), [202], 'create ' + site);
    await waitForJob(page, job.job_id);
  }
  for (const [site, domain] of routes) {
    await expectStatus(await call(page, 'POST', '/api/sites/deploy', { site, domain }), [200, 201, 202], `route ${domain}`);
  }
  for (const site of ['northwind', 'acme-shop', 'northwind']) {
    const job = await expectStatus(await call(page, 'POST', '/api/backups', { site, include_databases: false }), [202], 'backup ' + site);
    await waitForJob(page, job.job_id);
  }
  await expectStatus(await call(page, 'POST', '/api/accounts', { username: customer.username, password: customer.password, totp_secret: customer.totp, plan: customer.plan, sites: customer.sites }), [200, 201], 'customer account');
}

// show scrolls a section to the top of the viewport, below the sticky
// header, and lets asynchronous panels finish loading.
async function show(page, selector) {
  await page.locator(selector).first().waitFor();
  await page.evaluate((selector) => {
    document.documentElement.style.scrollBehavior = 'auto';
    const target = document.querySelector(selector);
    const header = document.querySelector('.topbar, header');
    window.scrollTo(0, target.getBoundingClientRect().top + window.scrollY - (header ? header.getBoundingClientRect().height + 16 : 16));
  }, selector);
  await page.waitForTimeout(700);
}

// settle waits for the dashboard's asynchronous panels. The page never
// reaches network idle because it holds the job event stream open.
async function settle(page) {
  await page.waitForLoadState('load');
  await page.waitForFunction(() => !/Loading/.test(document.body.innerText), null, { timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(1200);
}

// open loads a dashboard URL from scratch: the workspace reads its
// #site= hash only on page load, not on a same-document hash change.
async function open(page, url) {
  await page.goto('about:blank');
  await page.goto(base + url);
  await settle(page);
}

async function capture(page, name) {
  const file = path.join(outDir, name + '.png');
  await page.screenshot({ path: file });
  console.log('captured', path.relative(process.cwd(), file));
}

(async () => {
  fs.mkdirSync(outDir, { recursive: true });
  const browser = await chromium.launch(process.env.PLAYWRIGHT_CHANNEL ? { channel: process.env.PLAYWRIGHT_CHANNEL } : {});
  try {
    const context = await browser.newContext({ viewport: desktop, deviceScaleFactor: 2, colorScheme: 'dark' });
    await context.addInitScript((preferences) => {
      window.localStorage.setItem('stepanel.appearance.v1', JSON.stringify(preferences));
    }, retroNeonPreferences);
    const page = await context.newPage();

    await page.goto(base + '/login');
    await capture(page, 'login');

    await signIn(page, admin);
    await seed(page);

    await page.goto(base + '/');
    await settle(page);
    await capture(page, 'operator-overview');

    await show(page, '#sites');
    await capture(page, 'sites');

    await open(page, '/#site=northwind&tab=domains');
    await show(page, '#siteDetail');
    await capture(page, 'site-workspace');

    await open(page, '/#site=northwind&tab=backups');
    await show(page, '#siteDetail');
    await capture(page, 'site-backups');

    await open(page, '/#jobs');
    await show(page, '#jobs');
    await capture(page, 'activity');

    const phone = await browser.newContext({ viewport: mobile, deviceScaleFactor: 3, colorScheme: 'dark', isMobile: true, hasTouch: true });
    await phone.addInitScript((preferences) => {
      window.localStorage.setItem('stepanel.appearance.v1', JSON.stringify(preferences));
    }, retroNeonPreferences);
    const phonePage = await phone.newPage();
    await signIn(phonePage, admin);
    await settle(phonePage);
    await capture(phonePage, 'operator-overview-mobile');

    const tenant = await browser.newContext({ viewport: desktop, deviceScaleFactor: 2, colorScheme: 'dark' });
    await tenant.addInitScript((preferences) => {
      window.localStorage.setItem('stepanel.appearance.v1', JSON.stringify(preferences));
    }, retroNeonPreferences);
    const tenantPage = await tenant.newPage();
    await signIn(tenantPage, { username: customer.username, password: customer.password, code: totp(customer.totp) });
    await settle(tenantPage);
    await capture(tenantPage, 'customer-overview');
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exit(1);
});
