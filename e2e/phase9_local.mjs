// DrawSpace shell + local accounts E2E in real Chrome: the login page, local admin/user accounts, user management UI, local<->SSO
// collaboration, lockout, disable, the single header, status pill, History menu inside draw.io's menu bar, comments switched off,
// copy-as-PNG, and the unsaved-work guard (no browser pop-up when saved). Run: node e2e/phase9_local.mjs
import { BASE, check, finish, launch, newPage, keycloakLogin, localAdminPassword } from './lib.mjs';

const browser = await launch();
const sleep = ms => new Promise(r => setTimeout(r, ms));
const tag = String(Date.now()).slice(-6);
const LU = 'e2e-lu' + tag, LA = 'e2e-la' + tag, PW1 = 'First-Pass-' + tag, PW2 = 'Second-Pass-' + tag;

async function localLogin(user, pass, ret) {
  const s = await newPage(browser);
  await s.page.goto(BASE + (ret || '/'), { waitUntil: 'domcontentloaded' });
  await s.page.waitForSelector('#f');
  await s.page.fill('#username', user); await s.page.fill('#password', pass);
  if (await s.page.locator('#local-btn').count()) await s.page.click('#local-btn'); else await s.page.click('#submit');
  await s.page.waitForURL(u => !/^\/login$/.test(u.pathname), { timeout: 20000 });
  s.csrf = (await s.page.evaluate(() => fetch('/api/me').then(r => r.json()))).csrf;
  return s;
}
async function ssoLogin(user) {
  const s = await newPage(browser);
  await s.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(s.page, user);
  await s.page.waitForURL(BASE + '/', { timeout: 30000 });
  await s.page.waitForSelector('#list-view:not([hidden])');
  s.csrf = (await s.page.evaluate(() => fetch('/api/me').then(r => r.json()))).csrf;
  return s;
}
const api = (s, m, p, body) => s.page.evaluate(async ([m, p, body, c]) => { const r = await fetch('/api/v1' + p, { method: m, headers: { 'X-CSRF-Token': c, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }); const t = await r.text(); let j = null; try { j = JSON.parse(t); } catch (e) { /* text */ } return { status: r.status, json: j }; }, [m, p, body, s.csrf]);
const ready = s => s.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status').dataset.state === 'ready' && window.__adapter.presence(), null, { timeout: 90000 });

// ---- the login page ----------------------------------------------------------------------------------------------------------------------------------
{
  const s = await newPage(browser);
  await s.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await s.page.waitForSelector('#f');
  check('LOGIN PAGE: DrawSpace branding, AD BTN and local buttons', (await s.page.title()).includes('DrawSpace') && (await s.page.textContent('.wordmark')).includes('DrawSpace') && await s.page.isVisible('#local-btn') && await s.page.isVisible('#submit') && (await s.page.textContent('.tagline')).includes('COLLABORATE'));
  const csp = (await s.ctx.request.get(BASE + '/login')).headers()['content-security-policy'];
  check('LOGIN PAGE: strict CSP (no unsafe-inline, script from self only)', !/unsafe-inline/.test(csp) && /script-src 'self'/.test(csp), csp);
  await s.page.fill('#username', 'nosuchuser' + tag); await s.page.fill('#password', 'whatever-it-is');
  await s.page.click('#submit');
  await s.page.waitForSelector('#err:not([hidden])');
  check('LOGIN PAGE: wrong credentials show a generic message (no user enumeration)', /invalid username or password/i.test(await s.page.textContent('#err')));
  for (let i = 0; i < 5; i++) { await s.page.click('#submit'); await sleep(250); }
  await s.page.waitForFunction(() => /too many attempts/i.test(document.getElementById('err').textContent), null, { timeout: 8000 })
    .then(() => check('LOGIN PAGE: repeated failures lock the username for a while (429 message)', true), () => check('LOGIN PAGE: repeated failures lock the username for a while (429 message)', false, 'last: ' + 'n/a'));
  await s.page.fill('#password', 'x'); await s.page.click('#toggle');
  check('LOGIN PAGE: show/hide password', (await s.page.getAttribute('#password', 'type')) === 'text');
  await s.ctx.close();
}

// ---- local administrator: manage users from the UI ---------------------------------------------------------------------------------------------------------------
const admin = await localLogin('admin', localAdminPassword());
await admin.page.waitForSelector('#list-view:not([hidden])');
check('LOCAL ADMIN: signs in with the local form', true);
check('LOCAL ADMIN: header shows DrawSpace, the save status and the user menu with the administrator badge', (await admin.page.textContent('.wordmark')).includes('DrawSpace') && await admin.page.isVisible('#status') && await admin.page.isVisible('#role'));
await admin.page.click('#userbtn');
check('LOCAL ADMIN: user menu: local account, Manage users, Change password, Sign out', /local account/.test(await admin.page.textContent('#menu-sub')) && await admin.page.isVisible('#mi-users') && await admin.page.isVisible('#mi-password') && await admin.page.isVisible('#logout'));
await admin.page.click('#mi-users');
await admin.page.waitForURL(/\/admin\/users$/); await admin.page.waitForSelector('#users-view:not([hidden])');
check('USERS: the admin page lists the local accounts', await admin.page.waitForSelector('#users-body tr[data-user="admin"]', { timeout: 8000 }).then(() => true).catch(() => false));
await admin.page.click('#new-user');
await admin.page.fill('#nu-username', 'lab-test2'); await admin.page.fill('#nu-name', 'Clash'); await admin.page.fill('#nu-password', PW1);
await admin.page.click('#dlg-actions .primary');
await admin.page.waitForSelector('#dlg .msg:not([hidden])', { timeout: 8000 });
check('USERS: a username that belongs to an SSO account is refused', /SSO/i.test(await admin.page.textContent('#dlg .msg')), await admin.page.textContent('#dlg .msg'));
await admin.page.fill('#nu-username', LU); await admin.page.fill('#nu-name', 'Local User ' + tag);
await admin.page.click('#dlg-actions .primary');
await admin.page.waitForSelector(`#users-body tr[data-user="${LU}"]`, { timeout: 8000 });
check('USERS: create a local user from the UI', true);
await admin.page.click('#new-user'); await admin.page.fill('#nu-username', LU); await admin.page.fill('#nu-password', PW1); await admin.page.click('#dlg-actions .primary');
check('USERS: duplicate username refused', await admin.page.waitForSelector('#dlg .msg:not([hidden])', { timeout: 8000 }).then(() => true).catch(() => false));
await admin.page.click('#dlg-actions button:not(.primary)');
check('USERS: weak password refused by the server', (await api(admin, 'POST', '/admin/local-users', { username: LA, role: 'admin', password: 'short' })).status === 400);
check('USERS: create a second local admin through the API', (await api(admin, 'POST', '/admin/local-users', { username: LA, name: 'Second Admin', role: 'admin', password: PW1 })).status === 201);

// ---- a local user: no admin powers, creates a diagram, shares with an SSO user, collaborates ----------------------------------------------------------------------------------------
await admin.page.click('#userbtn'); await admin.page.click('#logout');
await admin.page.waitForURL(/\/logged-out$/, { timeout: 15000 });
check('SIGN OUT: a local session ends and lands on the signed-out page', (await admin.page.textContent('body')).includes('signed out'));
const lu = await localLogin(LU, PW1);
await lu.page.click('#userbtn');
check('LOCAL USER: no "Manage users" entry, but Change password is there', !(await lu.page.isVisible('#mi-users')) && await lu.page.isVisible('#mi-password'));
await lu.page.keyboard.press('Escape');
check('LOCAL USER: the admin API is refused', (await api(lu, 'GET', '/admin/local-users')).status === 403);
await lu.page.goto(BASE + '/admin/users');
check('LOCAL USER: /admin/users shows no data to a non-admin', (await lu.page.locator('#users-body tr').count()) === 0);
await lu.page.goto(BASE + '/');
await lu.page.waitForSelector('#list-view:not([hidden])');
await lu.page.click('#new'); await lu.page.waitForSelector('#dlg[open] input');
await lu.page.fill('#dlg input', 'Local + SSO ' + tag); await lu.page.keyboard.press('Enter');
await lu.page.waitForURL(/\/d\/[a-z0-9]+$/, { timeout: 15000 });
const id = lu.page.url().split('/d/')[1];
await ready(lu);
check('EDITOR: a local user creates and opens a diagram (Enter in the title dialog)', true);

// one header, no second toolbar; save pill; History inside draw.io's own menu bar; comments off
check('ONE BAR: no separate sub-bar: title, role and Share sit in the header', (await lu.page.locator('.subbar').count()) === 0 && await lu.page.isVisible('#title') && await lu.page.isVisible('#btn-share') && await lu.page.isVisible('#rolebadge'));
check('SAVE STATUS: the pill says "All changes saved" in the header, readable (not only a dot)', /all changes saved/i.test(await lu.page.textContent('#status-text')));
check('COMMENTS: switched off (no button, no menu entry), the code stays behind a flag', (await lu.page.locator('#btn-comments').count()) === 0 && await lu.page.evaluate(() => { const ui = window.__adapter.editorUi(); return !/Comments/.test(ui.menubar.container.textContent); }));
const frame = lu.page.frameLocator('#editor');
await frame.getByText('History', { exact: true }).first().click();
await frame.getByText('Change history').click();
check('HISTORY MENU: "History" lives in draw.io\'s own menu bar and opens the history panel', await lu.page.waitForSelector('#panel:not([hidden])', { timeout: 8000 }).then(() => /History/.test('')||true).catch(() => false) && (await lu.page.textContent('#panel-title')) === 'History');
await lu.page.click('#panel-close');

// share with an SSO user and collaborate
await lu.page.click('#btn-share');
await lu.page.waitForSelector('#dlg[open] #btn-add-user');
await lu.page.click('#btn-add-user');
await lu.page.fill('#share-q', 'lab-test2');
await lu.page.waitForSelector('.hit[data-user="lab-test2"]', { timeout: 8000 });
await lu.page.click('.hit[data-user="lab-test2"]');
await lu.page.waitForSelector('[data-remove="lab-test2"]');
await lu.page.click('#dlg-actions .primary');
const sso = await ssoLogin('lab-test2');
check('LOCAL <-> SSO: the diagram of a local user shows up in the SSO user\'s list with their role', (await api(sso, 'GET', '/documents')).json.documents.some(d => d.id === id && d.owner === LU && d.role === 'editor'));
await sso.page.goto(BASE + '/d/' + id); await ready(sso);
await lu.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.getModel().beginUpdate(); try { g.insertVertex(g.getDefaultParent(), 'L1', 'from local', 40, 40, 120, 60); } finally { g.getModel().endUpdate(); } });
await sso.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.getModel().beginUpdate(); try { g.insertVertex(g.getDefaultParent(), 'S1', 'from sso', 240, 40, 120, 60); } finally { g.getModel().endUpdate(); } });
const both = s => s.page.waitForFunction(() => { const m = window.__adapter.editorUi().editor.graph.getModel(); return !!m.getCell('L1') && !!m.getCell('S1'); }, null, { timeout: 15000 }).then(() => true).catch(() => false);
check('LOCAL <-> SSO: both see each other\'s shapes in real time', (await both(lu)) && (await both(sso)));
check('LOCAL <-> SSO: and see each other in the presence bar', await lu.page.waitForFunction(() => document.querySelectorAll('#presence .pav').length >= 2, null, { timeout: 10000 }).then(() => true).catch(() => false));
const hist = (await api(lu, 'GET', `/documents/${id}/history?limit=20`)).json.history;
check('LOCAL <-> SSO: history attributes each change to the right person (local and SSO names)', hist.some(h => h.actor === LU) && hist.some(h => h.actor === 'lab-test2'), hist.map(h => h.actor).join());

// ---- copy as PNG (clipboard) ---------------------------------------------------------------------------------------------------------------------------------------------------------
await lu.ctx.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: BASE });
await lu.page.bringToFront();
await lu.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.setSelectionCell(g.getModel().getCell('L1')); });
await frame.getByText('Edit', { exact: true }).first().click();
check('COPY AS IMAGE: the Edit menu offers PNG, SVG and JPG for the selection', await frame.getByText('Copy as PNG image').isVisible() && await frame.getByText('Copy as SVG').isVisible() && await frame.getByText('Save selection as JPG…').isVisible());
await frame.getByText('Copy as PNG image').click();
await sleep(1200);
const clip = await lu.page.evaluate(async () => { try { const items = await navigator.clipboard.read(); return items.map(i => i.types.join('+')).join(','); } catch (e) { return 'ERR ' + e.message; } });
check('COPY AS IMAGE: "Copy as PNG image" puts a PNG on the clipboard', /image\/png/.test(clip), clip);
await lu.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.setSelectionCell(g.getModel().getCell('L1')); });

// ---- the unsaved-work guard ---------------------------------------------------------------------------------------------------------------------------------------------------
let native = 0; lu.page.on('dialog', d => { native++; d.dismiss(); });
check('LEAVE GUARD: when everything is saved there is no pop-up and the page just leaves', await (async () => {
  await lu.page.evaluate(() => window.__adapter.flush());
  await lu.page.waitForFunction(() => !window.__app.dirty(), null, { timeout: 10000 });
  await lu.page.click('#home');
  await lu.page.waitForURL(BASE + '/', { timeout: 10000 });
  return native === 0 && !(await lu.page.isVisible('#dlg[open]'));
})());
await lu.page.goto(BASE + '/d/' + id); await ready(lu);
await lu.page.evaluate(() => { WebSocket.prototype.__send = WebSocket.prototype.send; WebSocket.prototype.send = function (d) { if (typeof d === 'string' && d.indexOf('"t":"ops"') > 0) return; return this.__send(d); }; }); // simulate: the server never receives it
await lu.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.getModel().beginUpdate(); try { g.insertVertex(g.getDefaultParent(), 'L2', 'not saved', 40, 200, 100, 40); } finally { g.getModel().endUpdate(); } });
await lu.page.waitForFunction(() => window.__app.dirty(), null, { timeout: 8000 });
check('LEAVE GUARD: while changes are pending the pill says "Saving…"', /saving/i.test(await lu.page.textContent('#status-text')), await lu.page.textContent('#status-text'));
await lu.page.click('#home');
check('LEAVE GUARD: with unsaved changes our own dialog appears (not the browser\'s)', await lu.page.waitForSelector('#dlg[open]', { timeout: 5000 }).then(() => true).catch(() => false) && /still being saved/i.test(await lu.page.textContent('#dlg-title')) && native === 0, await lu.page.textContent('#dlg-title'));
await lu.page.click('#dlg-actions button:not(.primary)'); // Cancel = stay
check('LEAVE GUARD: "Cancel" keeps the editor open', /\/d\//.test(lu.page.url()));
await lu.page.evaluate(() => { WebSocket.prototype.send = WebSocket.prototype.__send; });
let nativeSeen = 0; lu.page.removeAllListeners('dialog'); lu.page.on('dialog', d => { nativeSeen++; d.accept(); }); // tab close / reload with really unsaved work: the browser's own prompt is the last line of defence

// ---- change password, sign in again ---------------------------------------------------------------------------------------------------------------------------------------------
await lu.page.goto(BASE + '/'); await lu.page.waitForSelector('#list-view:not([hidden])');
await lu.page.click('#userbtn'); await lu.page.click('#mi-password');
await lu.page.fill('#pw-current', 'wrong-password-here'); await lu.page.fill('#pw-new', PW2); await lu.page.fill('#pw-repeat', PW2);
await lu.page.click('#dlg-actions .primary');
check('PASSWORD: a wrong current password is refused', await lu.page.waitForSelector('#dlg .msg:not([hidden])', { timeout: 8000 }).then(() => true).catch(() => false));
await lu.page.fill('#pw-current', PW1); await lu.page.click('#dlg-actions .primary');
await lu.page.waitForSelector('#dlg[open]', { state: 'detached', timeout: 8000 }).catch(() => {});
check('PASSWORD: change succeeds', await lu.page.locator('.toast', { hasText: 'Password changed' }).first().waitFor({ timeout: 8000 }).then(() => true).catch(() => false));
const bad = await newPage(browser);
await bad.page.goto(BASE + '/'); await bad.page.waitForSelector('#f'); await bad.page.fill('#username', LU); await bad.page.fill('#password', PW1); await bad.page.click('#submit');
check('PASSWORD: the old password no longer works', await bad.page.waitForSelector('#err:not([hidden])', { timeout: 8000 }).then(() => true).catch(() => false));
await bad.ctx.close();
const lu2 = await localLogin(LU, PW2);
check('PASSWORD: the new password signs in', true);

// ---- disable ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
const adm2 = await localLogin(LA, PW1);
check('LOCAL ADMIN (second): admin role comes from the database', (await api(adm2, 'GET', '/admin/local-users')).status === 200);
check('DISABLE: the admin disables the user', (await api(adm2, 'PATCH', `/admin/local-users/${LU}`, { disabled: true })).status === 200);
let blocked = false;
for (let i = 0; i < 25 && !blocked; i++) { blocked = (await api(lu2, 'GET', '/documents')).status === 403; if (!blocked) await sleep(1000); }
check('DISABLE: an already signed-in session of a disabled account is cut off (within the 15 s cache)', blocked);
const dis = await newPage(browser);
await dis.page.goto(BASE + '/'); await dis.page.waitForSelector('#f'); await dis.page.fill('#username', LU); await dis.page.fill('#password', PW2); await dis.page.click('#submit');
check('DISABLE: and it cannot sign in again', await dis.page.waitForSelector('#err:not([hidden])', { timeout: 8000 }).then(() => true).catch(() => false));
await dis.ctx.close();
check('GUARD: an administrator cannot disable or demote their own account', (await api(adm2, 'PATCH', `/admin/local-users/${LA}`, { disabled: true })).status === 409);

// ---- cleanup --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
await api(adm2, 'PATCH', `/admin/local-users/${LU}`, { disabled: false });
const luCleanup = await localLogin(LU, PW2);
await api(luCleanup, 'DELETE', `/documents/${id}`);
check('CLEANUP: delete the test accounts', (await api(adm2, 'DELETE', `/admin/local-users/${LU}`)).status === 200);
const adminAgain = await localLogin('admin', localAdminPassword());
check('CLEANUP: the other test admin', (await api(adminAgain, 'DELETE', `/admin/local-users/${LA}`)).status === 200);

await browser.close();
finish();
