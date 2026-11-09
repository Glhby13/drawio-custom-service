// Phase 16 E2E (real Chrome, lab): invitation-based sharing (ADR-0021): the Share dialog, the e-mailed invitation (read back from
// Mailpit), accept / decline on /invite/<token>, link invitations, the daily e-mail limit, expiry bounds and the refused direct share.
// Run: node e2e/phase16_share_invite.mjs   (needs the platform + gateway with ADR-0021, Mailpit, lab test users from lab/SECRETS.md)
import https from 'node:https';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { BASE, check, finish, launch, newPage, keycloakLogin } from './lib.mjs';

const MAILPIT = process.env.MAILPIT_BASE || 'https://mailpit.lab.local';
// lab-only credential, read from lab/SECRETS.md at run time (row "Mailpit"): never written into git
const mailpitPassword = () => { const row = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'SECRETS.md'), 'utf8').split('\n').find(l => /mailpit/i.test(l) && /`admin`/.test(l)); const m = row && [...row.split('|').slice(2).join('|').matchAll(/`([^`]+)`/g)].map(x => x[1]).find(v => v !== 'admin' && /Mailpit/i.test(v)); if (!m) throw new Error('Mailpit password not found in SECRETS.md'); return m; };
const MAILPIT_AUTH = 'Basic ' + Buffer.from('admin:' + mailpitPassword()).toString('base64');
const agent = new https.Agent({ rejectUnauthorized: false });
function mailpit(path) {
  return new Promise((resolve, reject) => {
    const req = https.get(MAILPIT + path, { agent, headers: { Authorization: MAILPIT_AUTH } }, res => {
      let b = ''; res.on('data', c => (b += c));
      res.on('end', () => { try { resolve(JSON.parse(b)); } catch (e) { reject(new Error('mailpit ' + res.statusCode + ' ' + b.slice(0, 120))); } });
    });
    req.on('error', reject);
    req.setTimeout(15000, () => req.destroy(new Error('mailpit timeout')));
  });
}
// the newest message with exactly this subject (polls: the MTA hand-off is asynchronous)
async function findMail(subject, timeoutMs = 45000) {
  const until = Date.now() + timeoutMs;
  while (Date.now() < until) {
    try {
      const list = await mailpit('/api/v1/messages?limit=100');
      const m = (list.messages || []).find(x => x.Subject === subject);
      if (m) return mailpit('/api/v1/message/' + m.ID);
    } catch (e) { /* retry */ }
    await new Promise(r => setTimeout(r, 1500));
  }
  return null;
}

const browser = await launch();

async function login(user) {
  const s = await newPage(browser);
  await s.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(s.page, user);
  await s.page.waitForURL(BASE + '/', { timeout: 30000 });
  await s.page.waitForSelector('#list-view:not([hidden])', { timeout: 20000 });
  s.me = await s.page.evaluate(() => fetch('/api/me', { credentials: 'same-origin' }).then(r => r.json()));
  s.csrf = s.me.csrf;
  return s;
}
const api = (s, method, path, body) => s.page.evaluate(async ({ method, path, body, csrf }) => {
  const r = await fetch('/api/v1' + path, { method, credentials: 'same-origin', headers: Object.assign({ 'X-CSRF-Token': csrf }, body !== undefined ? { 'Content-Type': 'application/json' } : {}), body: body !== undefined ? JSON.stringify(body) : undefined });
  const t = await r.text(); let j = null; try { j = JSON.parse(t); } catch (e) { /* text */ }
  return { status: r.status, json: j, text: t };
}, { method, path, body, csrf: s.csrf });
const errCode = r => (r.json && r.json.error && r.json.error.code) || '';
const inList = (r, id) => (r.json && r.json.documents || []).some(d => d.id === id);
const roleIn = (r, id) => ((r.json && r.json.documents || []).find(d => d.id === id) || {}).role;
const DAY = 86400000;

const owner = await login('e2e-team1');
const recip = await login('lab-test');
const stranger = await login('e2e-user2');
const stranger2 = await login('e2e-team2');
const tag = String(Date.now()).slice(-7);
const title = 'Invite E2E ' + tag, title2 = 'Invite E2E B ' + tag;
const created = [];
const mk = async t => { const r = await api(owner, 'POST', '/documents', { title: t }); const id = r.json && r.json.document && r.json.document.id; if (id) created.push(id); return { r, id }; };

// ---- (1) a diagram
const { r: made, id } = await mk(title);
check('(1) owner creates a diagram', made.status === 201 && id, 'status ' + made.status);

// ---- (2) the Share dialog: no "Share with everyone", no commenter
await owner.page.goto(BASE + '/d/' + id, { waitUntil: 'domcontentloaded' });
await owner.page.waitForSelector('#btn-share', { timeout: 40000 });
await owner.page.click('#btn-share');
await owner.page.waitForSelector('#dlg[open]');
const dlgText = await owner.page.locator('#dlg').innerText();
check('(2) the Share dialog has no "Share with everyone" button and no commenter option', !/everyone/i.test(dlgText) && !/commenter/i.test(dlgText) && (await owner.page.locator('#dlg button', { hasText: /everyone/i }).count()) === 0, dlgText.slice(0, 120).replace(/\s+/g, ' '));

// ---- (3) invite lab-test through the dialog; the mail arrives, the diagram does not (yet)
await owner.page.click('#btn-add-user');
await owner.page.fill('#share-q', 'lab-test');
const hit = owner.page.locator('button.hit[data-user="lab-test"]');
await hit.waitFor({ timeout: 15000 });
await hit.click();
await owner.page.waitForFunction(() => { const b = document.getElementById('share-send'); return b && !b.disabled; }, null, { timeout: 5000 }).catch(() => {});
const sendEnabled = await owner.page.locator('#share-send').isEnabled();
check('(3a) the picked person (lab-test) can be invited by e-mail (an address is known)', sendEnabled);
let token = null, mail = null;
if (sendEnabled) {
  await owner.page.click('#share-send');
  const toast = owner.page.locator('.toast', { hasText: /e-mailed/i });
  await toast.first().waitFor({ timeout: 30000 }).catch(() => {});
  check('(3b) the toast says the invitation was e-mailed', (await toast.count()) > 0, (await owner.page.locator('.toast').allInnerTexts()).join(' | '));
  check('(3c) the diagram is NOT in lab-test\'s list before acceptance', !inList(await api(recip, 'GET', '/documents'), id) && (await api(recip, 'GET', '/documents/' + id)).status !== 200);
  const subject = "Undangan akses diagram '" + title + "'";
  mail = await findMail(subject);
  check('(3d) Mailpit has the invitation e-mail with the expected subject', !!mail, subject);
  if (mail) {
    const html = mail.HTML || '', text = mail.Text || '';
    const to = (mail.To || []).map(t => t.Address).join(',');
    check('(3e) From is DrawSpace <noreply@btn.co.id> and the recipient address is set', mail.From && mail.From.Address === 'noreply@btn.co.id' && /drawspace/i.test(mail.From.Name || '') && !!to, JSON.stringify(mail.From) + ' -> ' + to);
    check('(3f) the HTML has the title, the inviter, the CTA "Lihat Undangan" and Indonesian wording', html.includes(title) && (html.includes(owner.me.username) || (owner.me.name && html.includes(owner.me.name))) && html.includes('Lihat Undangan') && /mengundang|akses/i.test(html), 'len ' + html.length);
    const m = /\/invite\/(dsi_[A-Za-z0-9_-]{43})/.exec(html) || /\/invite\/(dsi_[A-Za-z0-9_-]{43})/.exec(text);
    token = m && m[1];
    check('(3g) the mail carries a /invite/dsi_... link', !!token);
  }
}

// ---- (4) a stranger cannot use the token
if (token) {
  const pv = await api(stranger, 'GET', '/invitations/' + token);
  check('(4) a stranger previewing the token gets 403 and no title in the body, and has no access', pv.status === 403 && !pv.text.includes(title) && !inList(await api(stranger, 'GET', '/documents'), id) && (await api(stranger, 'GET', '/documents/' + id)).status !== 200, 'status ' + pv.status);

  // ---- (5) the recipient accepts in the UI
  await recip.page.goto(BASE + '/invite/' + token, { waitUntil: 'domcontentloaded' });
  await recip.page.waitForSelector('#invite-accept', { timeout: 20000 });
  const card = await recip.page.locator('#invite-card').innerText();
  check('(5a) the invite page shows title and role in Indonesian with accept/decline', card.includes(title) && /Editor \(dapat melihat/.test(await recip.page.locator('#invite-role').innerText()) && (await recip.page.locator('#invite-decline').count()) === 1, card.slice(0, 140).replace(/\s+/g, ' '));
  await recip.page.click('#invite-accept');
  await recip.page.waitForURL(u => u.pathname === '/d/' + id, { timeout: 20000 }).catch(() => {});
  check('(5b) accepting lands on /d/<id>', new URL(recip.page.url()).pathname === '/d/' + id, recip.page.url());
  const rl = await api(recip, 'GET', '/documents');
  check('(5c) the diagram is now in lab-test\'s list with role editor', inList(rl, id) && roleIn(rl, id) === 'editor', String(roleIn(rl, id)));
  const pl = await api(owner, 'GET', '/documents/' + id + '/permissions');
  check('(5d) the owner\'s permission list contains lab-test', ((pl.json && pl.json.permissions) || []).some(p => p.subject === 'lab-test'));
  check('(5e) the used user-token cannot be accepted again (404)', (await api(recip, 'POST', '/invitations/' + token + '/accept')).status === 404);
}

// ---- (6) one e-mail per person and diagram per day
{
  const rm = await api(owner, 'PUT', '/documents/' + id + '/permissions', { subject: 'lab-test', role: '' });
  check('(6a) the owner can remove lab-test\'s access directly', rm.status === 200 || rm.status === 204, 'status ' + rm.status);
  const again = await api(owner, 'POST', '/documents/' + id + '/invitations', { kind: 'user', subject: 'lab-test', role: 'viewer', sendEmail: true });
  if (mail) check('(6b) a second e-mailed invitation the same day is refused: 429 email_cooldown', again.status === 429 && errCode(again) === 'email_cooldown', again.status + ' ' + errCode(again));
  else check('(6b) skipped-by-failure: no first mail was sent, so no cooldown can be tested', false, again.status + ' ' + errCode(again));
  const quiet = await api(owner, 'POST', '/documents/' + id + '/invitations', { kind: 'user', subject: 'lab-test', role: 'viewer', sendEmail: false });
  check('(6c) the same invitation without an e-mail still works (201)', quiet.status === 201 && !!(quiet.json && quiet.json.token), 'status ' + quiet.status);
}

// ---- (7) decline
const { id: id2 } = await mk(title2);
{
  const inv = await api(owner, 'POST', '/documents/' + id2 + '/invitations', { kind: 'user', subject: 'lab-test', role: 'viewer', sendEmail: false });
  const t = inv.json && inv.json.token;
  check('(7a) owner invites lab-test (viewer, no e-mail)', inv.status === 201 && !!t, 'status ' + inv.status);
  if (t) {
    await recip.page.goto(BASE + '/invite/' + t, { waitUntil: 'domcontentloaded' });
    await recip.page.waitForSelector('#invite-decline', { timeout: 20000 });
    await recip.page.click('#invite-decline');
    await recip.page.waitForFunction(() => /Anda menolak undangan/.test(document.getElementById('invite-card').innerText), null, { timeout: 15000 }).catch(() => {});
    check('(7b) declining shows the decline text', /Anda menolak undangan/.test(await recip.page.locator('#invite-card').innerText()));
    check('(7c) no access after declining', !inList(await api(recip, 'GET', '/documents'), id2) && (await api(recip, 'GET', '/documents/' + id2)).status !== 200);
    const re = await api(recip, 'POST', '/invitations/' + t + '/accept');
    check('(7d) a declined token cannot be reused (404)', re.status === 404 && errCode(re) === 'invitation_invalid', re.status + ' ' + errCode(re));
  }
}

// ---- (8) one link, two people
{
  const lk = await api(owner, 'POST', '/documents/' + id2 + '/invitations', { kind: 'link', role: 'viewer', expiresInDays: 1 });
  const t = lk.json && lk.json.token, iid = lk.json && lk.json.invitation && lk.json.invitation.id;
  check('(8a) owner creates a link invitation (viewer, 1 day)', lk.status === 201 && !!t && !!iid, 'status ' + lk.status);
  if (t) {
    const a1 = await api(stranger, 'POST', '/invitations/' + t + '/accept');
    const a2 = await api(stranger2, 'POST', '/invitations/' + t + '/accept');
    check('(8b) two different users accept the same link', a1.status === 200 && a2.status === 200, a1.status + ' / ' + a2.status);
    check('(8c) both are viewers', roleIn(await api(stranger, 'GET', '/documents'), id2) === 'viewer' && roleIn(await api(stranger2, 'GET', '/documents'), id2) === 'viewer');
    const ls = await api(owner, 'GET', '/documents/' + id2 + '/invitations');
    const row = ((ls.json && ls.json.invitations) || []).find(i => i.id === iid) || {};
    check('(8d) the owner\'s list shows use count 2', (row.useCount !== undefined ? row.useCount : row.use_count) === 2, JSON.stringify(row));
    const rv = await api(owner, 'DELETE', '/documents/' + id2 + '/invitations/' + iid);
    check('(8e) the owner revokes the link', rv.status === 200 && rv.json && rv.json.state === 'revoked', rv.status + ' ' + rv.text.slice(0, 60));
    const a3 = await api(recip, 'POST', '/invitations/' + t + '/accept');
    check('(8f) a third accept after revoking fails (404)', a3.status === 404, 'status ' + a3.status);
  }
}

// ---- (9) expiry bounds
{
  const post = days => api(owner, 'POST', '/documents/' + id2 + '/invitations', days === undefined ? { kind: 'link', role: 'viewer' } : { kind: 'link', role: 'viewer', expiresInDays: days });
  const r366 = await post(366), rNeg = await post(-1), r0 = await post(0), rDef = await post(undefined);
  check('(9a) expiresInDays 366 and -1 are refused (400)', r366.status === 400 && rNeg.status === 400, r366.status + ' / ' + rNeg.status);
  check('(9b) expiresInDays 0 means never (expiresAt null)', r0.status === 201 && r0.json.invitation.expiresAt === null, r0.status + ' ' + JSON.stringify(r0.json && r0.json.invitation && r0.json.invitation.expiresAt));
  const d = rDef.status === 201 ? (new Date(rDef.json.invitation.expiresAt).getTime() - Date.now()) / DAY : NaN;
  check('(9c) an omitted expiresInDays defaults to about 7 days', d > 6.9 && d < 7.1, 'days ahead ' + d);
}

// ---- (10) UI: custom expiry
{
  const before = new Set(((await api(owner, 'GET', '/documents/' + id2 + '/invitations')).json.invitations || []).map(i => i.id));
  await owner.page.goto(BASE + '/d/' + id2, { waitUntil: 'domcontentloaded' });
  await owner.page.waitForSelector('#btn-share', { timeout: 40000 });
  await owner.page.click('#btn-share');
  await owner.page.waitForSelector('#share-link-exp');
  await owner.page.selectOption('#share-link-exp', 'custom');
  await owner.page.fill('#share-link-days', '3');
  await owner.page.click('#share-make-link');
  await owner.page.waitForSelector('#share-created:not([hidden])', { timeout: 15000 });
  const box = await owner.page.locator('#share-created').innerText();
  const now = ((await api(owner, 'GET', '/documents/' + id2 + '/invitations')).json.invitations || []).filter(i => !before.has(i.id));
  const d = now.length === 1 && now[0].expiresAt ? (new Date(now[0].expiresAt).getTime() - Date.now()) / DAY : NaN;
  check('(10) the custom 3-day link shows an expiry in the created box and the API says about 3 days', /expires/.test(box) && d > 2.9 && d < 3.1, box.replace(/\s+/g, ' ').slice(0, 140) + ' | days ' + d);
}

// ---- (11) a person with no known e-mail address
{
  const ghost = 'zz-noone-' + tag;
  const r = await api(owner, 'POST', '/documents/' + id2 + '/invitations', { kind: 'user', subject: ghost, role: 'viewer', sendEmail: true });
  // The environment knows no account without an address for sure, but a name that exists nowhere has none: 422 no_email
  // (503 email_unavailable would mean the lab has no mailer configured, which the other checks would already show).
  check('(11) an unknown account with sendEmail:true is refused: 422 no_email', r.status === 422 && errCode(r) === 'no_email', r.status + ' ' + errCode(r));
}

// ---- (12) the old direct share is gone
{
  const put = body => api(owner, 'PUT', '/documents/' + id + '/permissions', body);
  const a = await put({ subject: 'e2e-team2', role: 'viewer' });
  const b = await put({ subject: '*', role: 'viewer' });
  const c = await put({ subject: 'e2e-team2', role: 'commenter' });
  check('(12a) a direct share for a new person is refused: 400 use_invitation', a.status === 400 && errCode(a) === 'use_invitation', a.status + ' ' + errCode(a));
  check('(12b) subject "*" is refused (400)', b.status === 400, 'status ' + b.status);
  check('(12c) role commenter is refused (400)', c.status === 400, 'status ' + c.status);
}

// ---- (13) cleanup
for (const d of created) await api(owner, 'DELETE', '/documents/' + d);
await browser.close();
finish();
