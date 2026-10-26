// Shared helpers for the lab E2E suites. Lab only: reads test-user passwords from lab/SECRETS.md at run time.
import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { chromium } from 'playwright-core';

export const BASE = process.env.DRAWIO_BASE || 'https://drawio.lab.local';
const here = dirname(fileURLToPath(import.meta.url));
const SECRETS = join(here, '..', '..', 'SECRETS.md');

export function password(user) {
  // A SECRETS.md row may list several users and several passwords in the same order (e.g. `e2e-team1`, `e2e-team2`);
  // the password cell is the one right after the cell that names the user.
  for (const line of readFileSync(SECRETS, 'utf8').split('\n')) {
    const cells = line.split('|');
    for (let k = 1; k < cells.length - 1; k++) {
      const users = [...cells[k].matchAll(/`([^`]+)`/g)].map(m => m[1]);
      const i = users.indexOf(user);
      if (i < 0) continue;
      const pws = [...cells[k + 1].matchAll(/`([^`]+)`/g)].map(m => m[1]);
      if (pws.length === users.length) return pws[i];
      if (users.length === 1 && pws.length >= 1) return pws[0];
    }
  }
  throw new Error('password for ' + user + ' not found in SECRETS.md');
}

// the first local administrator (lab/SECRETS.md row "DrawSpace local sign-in")
export function localAdminPassword() {
  const m = /local admin `admin` \/ `([^`]+)`/.exec(readFileSync(SECRETS, 'utf8'));
  if (!m) throw new Error('local admin password not found in SECRETS.md');
  return m[1];
}

export const results = [];
export function check(name, pass, evidence) {
  results.push({ name, pass: !!pass, evidence });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (evidence ? ' :: ' + evidence : ''));
}
export function finish() {
  const failed = results.filter(r => !r.pass).length;
  console.log(`\n${results.length - failed}/${results.length} passed`);
  process.exit(failed ? 1 : 0);
}

export async function launch() {
  return chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true, args: ['--ignore-certificate-errors'] });
}

export async function newPage(browser) {
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  const log = { requests: [], violations: [], consoleErrors: [] };
  page.on('request', r => log.requests.push(r.url()));
  page.on('console', m => { if (m.type() === 'error') log.consoleErrors.push(m.text().slice(0, 200)); });
  if (process.env.LOCAL_WEB) { // fast loop: serve the host assets from the working tree instead of the deployed image
    await ctx.route('**/static/*', route => {
      const name = new URL(route.request().url()).pathname.split('/').pop();
      const f = join(here, '..', 'internal', 'edge', 'web', name);
      try { route.fulfill({ body: readFileSync(f), headers: { 'content-type': name.endsWith('.js') ? 'text/javascript' : name.endsWith('.css') ? 'text/css' : 'text/plain', 'cache-control': 'no-store' } }); } catch (e) { route.continue(); }
    });
  }
  await ctx.addInitScript(() => document.addEventListener('securitypolicyviolation', e => { (window.__csp = window.__csp || []).push(e.violatedDirective + ' ' + e.blockedURI); }));
  return { ctx, page, log };
}

// ---- TOTP (the drawio client's browser flow requires OTP, like ArgoCD: drawio-browser-role-otp) ----------------------------------
// Test users are enrolled on the fly the first time they hit the "configure authenticator" page; the seed is kept in
// e2e/.otp-seeds.json (lab only, git-ignored, recorded in lab/SECRETS.md) and used for every later login.
import { createHmac } from 'node:crypto';
import { existsSync, writeFileSync } from 'node:fs';
const SEEDS = join(here, '.otp-seeds.json');
const LAST = join(here, '.otp-last.json');
// Keycloak refuses a TOTP code that was already used in the current 30 s window, so repeated sign-ins of the same test user wait for the next window.
async function freshCode(user, seed) {
  const last = existsSync(LAST) ? JSON.parse(readFileSync(LAST, 'utf8')) : {};
  for (;;) {
    const step = Math.floor(Date.now() / 30000);
    if (last[user] !== step) { last[user] = step; writeFileSync(LAST, JSON.stringify(last)); return totp(seed); }
    await new Promise(r => setTimeout(r, 1000));
  }
}
const seeds = () => (existsSync(SEEDS) ? JSON.parse(readFileSync(SEEDS, 'utf8')) : {});
function b32decode(s) {
  const A = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'; let bits = '';
  for (const c of s.replace(/[\s=]/g, '').toUpperCase()) bits += A.indexOf(c).toString(2).padStart(5, '0');
  const out = []; for (let i = 0; i + 8 <= bits.length; i += 8) out.push(parseInt(bits.slice(i, i + 8), 2));
  return Buffer.from(out);
}
export function totp(b32, at = Date.now()) {
  const ctr = Buffer.alloc(8); ctr.writeBigUInt64BE(BigInt(Math.floor(at / 30000)));
  const h = createHmac('sha1', b32decode(b32)).update(ctr).digest(); const o = h[h.length - 1] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1e6).padStart(6, '0');
}

// access token for an AI agent acting as `user` (lab: direct grant; users with an authenticator must send the TOTP too)
export async function agentToken(ctx, user, client = 'drawio-mcp-claude') {
  const form = { grant_type: 'password', client_id: client, username: user, password: password(user), scope: 'openid profile' };
  const seed = seeds()[user]; if (seed) form.totp = await freshCode(user, seed);
  const r = await ctx.request.post('https://auth.lab.local/realms/lab/protocol/openid-connect/token', { form, failOnStatusCode: false });
  if (r.status() !== 200) throw new Error('agent token for ' + user + ': ' + r.status() + ' ' + (await r.text()).slice(0, 160));
  return (await r.json()).access_token;
}

// the DrawSpace login page is the entry; "Sign in with AD BTN" authenticates with Keycloak directly
export async function keycloakLogin(page, user) {
  await page.waitForURL(u => /auth\.lab\.local/.test(u.host) || /^\/login$/.test(u.pathname), { timeout: 30000 });
  if (/^\/login$/.test(new URL(page.url()).pathname)) {
    await page.fill('#username', user);
    await page.fill('#password', password(user));
    const seed = seeds()[user];
    if (seed) {
      const code = await freshCode(user, seed);
      if (await page.locator('#totp').count()) {
        await page.fill('#totp', code);
      }
    }
    await page.click('#submit');
    const outcome = await Promise.race([
      page.waitForURL(u => !/^\/login$/.test(u.pathname), { timeout: 30000 }).then(() => 'done'),
      page.waitForSelector('#err:not([hidden])', { timeout: 5000 }).then(() => 'err'),
      page.waitForURL(/auth\.lab\.local/, { timeout: 5000 }).then(() => 'kc'),
    ]).catch(() => 'unknown');
    if (outcome === 'done') return;
    if (outcome !== 'kc') return;
  }
  await page.waitForURL(/auth\.lab\.local\/realms\/lab\/protocol\/openid-connect\/auth/, { timeout: 30000 });
  await page.fill('#username', user);
  await page.fill('#password', password(user));
  await page.click('#kc-login');
  // after the password: the OTP prompt, the one-time enrolment page, an access-denied page, or straight back to the app
  const step = await Promise.race([
    page.waitForSelector('#otp', { timeout: 20000 }).then(() => 'otp'),
    page.waitForSelector('#totp, #kc-totp-secret-key, #mode-manual', { timeout: 20000 }).then(() => 'setup'),
    page.waitForURL(u => !/auth\.lab\.local/.test(u.toString()), { timeout: 20000 }).then(() => 'done'),
    page.waitForFunction(() => /access denied|not allowed|forbidden/i.test(document.body.innerText), null, { timeout: 20000 }).then(() => 'denied'),
  ]).catch(() => 'unknown');
  if (step === 'otp') {
    const seed = seeds()[user];
    if (!seed) throw new Error('user ' + user + ' asks for an OTP but no seed is known (not enrolled by this suite)');
    await page.fill('#otp', await freshCode(user, seed)); await page.click('#kc-login');
  } else if (step === 'setup') {
    const manual = page.locator('#mode-manual'); if (await manual.count()) await manual.click();
    const secret = (await page.locator('#kc-totp-secret-key').textContent()).replace(/\s/g, '');
    const all = seeds(); all[user] = secret; writeFileSync(SEEDS, JSON.stringify(all, null, 1));
    await page.fill('#totp', await freshCode(user, secret));
    const label = page.locator('#userLabel'); if (await label.count()) await label.fill('e2e');
    await page.click('#saveTOTPBtn');
  }
}

export function kubectl(args) {
  return execFileSync('ssh', ['lab-bastion', 'kubectl ' + args], { encoding: 'utf8' });
}
