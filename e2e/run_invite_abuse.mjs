// Logs three lab users in (real Chrome), then runs scripts/security/test_invite_abuse.py with their session cookies.
// Run: node e2e/run_invite_abuse.mjs      (lab only; session cookies stay in memory and in the child's environment)
import { spawnSync } from 'node:child_process';
import { BASE, launch, newPage, keycloakLogin } from './lib.mjs';

const browser = await launch();
async function session(user) {
  const s = await newPage(browser);
  await s.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(s.page, user);
  await s.page.waitForURL(BASE + '/', { timeout: 30000 });
  await s.page.waitForSelector('#list-view:not([hidden])', { timeout: 20000 });
  const csrf = (await s.page.evaluate(() => fetch('/api/me', { credentials: 'same-origin' }).then(r => r.json()))).csrf;
  const ck = (await s.ctx.cookies()).find(c => /drawio_sid$/.test(c.name));
  return { cookie: ck.name + '=' + ck.value, csrf, user };
}
const owner = await session('e2e-team1'), recip = await session('lab-test'), stranger = await session('e2e-user2');
await browser.close();
const r = spawnSync('python3', ['-I', new URL('../scripts/security/test_invite_abuse.py', import.meta.url).pathname], {
  stdio: 'inherit',
  env: { ...process.env, DRAWIO_BASE: BASE, OWNER_COOKIE: owner.cookie, OWNER_CSRF: owner.csrf, RECIP_COOKIE: recip.cookie, RECIP_CSRF: recip.csrf, RECIP_USER: recip.user, STRANGER_COOKIE: stranger.cookie, STRANGER_CSRF: stranger.csrf, STRANGER_USER: stranger.user },
});
process.exit(r.status ?? 1);
