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

export async function keycloakLogin(page, user) {
  await page.waitForURL(/auth\.lab\.local\/realms\/lab\/protocol\/openid-connect\/auth/, { timeout: 30000 });
  await page.fill('#username', user);
  await page.fill('#password', password(user));
  await page.click('#kc-login');
}

export function kubectl(args) {
  return execFileSync('ssh', ['lab-bastion', 'kubectl ' + args], { encoding: 'utf8' });
}
