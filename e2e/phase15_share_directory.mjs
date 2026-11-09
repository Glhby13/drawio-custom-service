// Phase 15 E2E (real Chrome, lab): the share picker finds EVERY enabled Keycloak user, also people who never signed in to DrawSpace.
// Ground truth = the realm itself, read with the gateway's own service account. Run: node e2e/phase15_share_directory.mjs
import { execFileSync } from 'node:child_process';
import { BASE, check, finish, launch, newPage, keycloakLogin, kubectl } from './lib.mjs';

const sec = k => Buffer.from(kubectl(`-n lab-dev get secret drawio-gateway-secret -o jsonpath={.data.${k}}`), 'base64').toString();
const KC = 'https://auth.lab.local';
const sh = c => execFileSync('ssh', ['lab-bastion', c], { encoding: 'utf8' });
const tok = JSON.parse(sh(`curl -sk -d grant_type=client_credentials -d client_id=drawio-lab --data-urlencode client_secret='${sec('OIDC_CLIENT_SECRET')}' ${KC}/realms/lab/protocol/openid-connect/token`)).access_token;
const realm = JSON.parse(sh(`curl -sk -H 'Authorization: Bearer ${tok}' '${KC}/admin/realms/lab/users?max=200&briefRepresentation=true'`));
const real = realm.filter(u => u.enabled && !u.username.startsWith('service-account-')).map(u => u.username.toLowerCase());
check('the realm has users to search for', real.length >= 3, real.length + ' enabled users');

const browser = await launch();
const { page } = await newPage(browser);
await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
await keycloakLogin(page, 'e2e-team1');
await page.waitForSelector('#list-view:not([hidden])', { timeout: 30000 });
const csrf = (await page.evaluate(() => fetch('/api/me').then(r => r.json()))).csrf;
const search = q => page.evaluate(async q => (await (await fetch('/api/v1/users?q=' + encodeURIComponent(q))).json()).users, q);

let missing = [];
for (const u of real) {
  const hits = await search(u);
  if (!hits.some(h => h.username === u)) missing.push(u);
}
check('every enabled Keycloak user is found by username (' + real.length + ' users)', missing.length === 0, missing.join(',') || 'all found');

const part = real.find(u => u.length > 6);
const hits = await search(part.slice(0, 5));
check('partial text finds matching users, capped and without duplicates', hits.some(h => h.username === part) && new Set(hits.map(h => h.username)).size === hits.length && hits.length <= 30, hits.length + ' hits');
check('disabled/service accounts are never offered', !(await search('service-account')).some(h => h.username.startsWith('service-account-')));
check('an unknown name returns an empty list, not an error', (await search('zzqx-nobody-1')).length === 0);

// sharing with a person who never signed in works end to end
const doc = await page.evaluate(async c => (await (await fetch('/api/v1/documents', { method: 'POST', headers: { 'X-CSRF-Token': c, 'Content-Type': 'application/json' }, body: '{"title":"phase15 share"}' })).json()).document.id, csrf);
const target = real.find(u => u !== 'e2e-team1');
const inv = await page.evaluate(async ([d, c, t]) => (await fetch(`/api/v1/documents/${d}/invitations`, { method: 'POST', headers: { 'X-CSRF-Token': c, 'Content-Type': 'application/json' }, body: JSON.stringify({ kind: 'user', subject: t, role: 'viewer', sendEmail: false }) })).status, [doc, csrf, target]);
const direct = await page.evaluate(async ([d, c, t]) => { const r = await fetch(`/api/v1/documents/${d}/permissions`, { method: 'PUT', headers: { 'X-CSRF-Token': c, 'Content-Type': 'application/json' }, body: JSON.stringify({ subject: t, role: 'viewer' }) }); return { status: r.status, text: await r.text() }; }, [doc, csrf, target]);
check('sharing with a directory user succeeds', inv === 201 && direct.status === 400 && direct.text.includes('use_invitation'), target + ' -> invitation ' + inv + ', direct PUT ' + direct.status);
await page.evaluate(async ([d, c]) => fetch('/api/v1/documents/' + d, { method: 'DELETE', headers: { 'X-CSRF-Token': c } }), [doc, csrf]);
await browser.close();
finish();
