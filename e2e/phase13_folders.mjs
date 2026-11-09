// Personal folders E2E against the lab (ADR-0020): nested folders, filing diagrams (menu + drag), "Shared with me" and the
// recipient's own filing, privacy between users, deletion moving contents up, URL state, search flat list.
// Run: node e2e/phase13_folders.mjs   (needs platform + gateway with ADR-0020; lab test users from lab/SECRETS.md)
import { shareViaInvitation, BASE, check, finish, launch, newPage, keycloakLogin } from './lib.mjs';

const browser = await launch();
async function login(user) {
  const s = await newPage(browser);
  await s.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(s.page, user);
  await s.page.waitForURL(BASE + '/', { timeout: 30000 });
  await s.page.waitForSelector('#list-view:not([hidden])', { timeout: 20000 });
  s.csrf = (await s.page.evaluate(() => fetch('/api/me', { credentials: 'same-origin' }).then(r => r.json()))).csrf;
  return s;
}
const api = (s, method, path, body) => s.page.evaluate(async ({ method, path, body, csrf }) => {
  const r = await fetch('/api/v1' + path, { method, credentials: 'same-origin', headers: Object.assign({ 'X-CSRF-Token': csrf }, body !== undefined ? { 'Content-Type': 'application/json' } : {}), body: body !== undefined ? JSON.stringify(body) : undefined });
  const t = await r.text(); let j = null; try { j = JSON.parse(t); } catch (e) { /* text */ }
  return { status: r.status, json: j };
}, { method, path, body, csrf: s.csrf });
const row = (s, text) => s.page.locator('#docs .doc', { hasText: text });
const reload = async (s, qs = '') => { await s.page.goto(BASE + '/' + qs, { waitUntil: 'domcontentloaded' }); await s.page.waitForSelector('#list-view:not([hidden])'); await s.page.waitForFunction(() => document.querySelector('#docs .doc') || !document.getElementById('empty').hidden); };

const owner = await login('e2e-team1');
const peer = await login('e2e-user2');
const tag = String(Date.now()).slice(-7), top = 'Proyek ' + tag, sub = 'Sub ' + tag, title = 'Folder E2E ' + tag;

// ---- API: nested folders, rules
const f1 = await api(owner, 'POST', '/folders', { name: top });
const f2 = await api(owner, 'POST', '/folders', { name: sub, parentId: f1.json && f1.json.folder.id });
check('owner creates a folder and a subfolder', f1.status === 201 && f2.status === 201, f1.status + '/' + f2.status);
const id1 = f1.json.folder.id, id2 = f2.json.folder.id;
check('duplicate name in the same folder is refused (409)', (await api(owner, 'POST', '/folders', { name: top.toUpperCase() })).status === 409);
check('moving a folder into its own child is refused (400)', (await api(owner, 'PATCH', '/folders/' + id1, { parentId: id2 })).status === 400);
check('another user sees none of these folders', ((await api(peer, 'GET', '/folders')).json.folders || []).every(f => f.id !== id1 && f.id !== id2));
check('another user cannot rename or delete them (404)', (await api(peer, 'PATCH', '/folders/' + id1, { name: 'x' })).status === 404 && (await api(peer, 'DELETE', '/folders/' + id1)).status === 404);

// ---- a diagram, shared with the peer
const made = await api(owner, 'POST', '/documents', { title });
const doc = made.json.document.id;
await shareViaInvitation(api, owner, doc, peer, 'e2e-user2', 'viewer');

// ---- UI: root shows the folder; it opens; breadcrumb + URL state; the diagram is filed through the "Move to…" dialog
await reload(owner);
check('root lists the top folder and the diagram', (await row(owner, top).count()) === 1 && (await row(owner, title).count()) === 1);
await row(owner, top).locator('a.doc-link').click();
check('opening a folder keeps the location in the URL (?folder=)', owner.page.url().includes('folder=' + id1), owner.page.url());
check('the breadcrumb shows Diagrams › folder', /Diagrams/.test(await owner.page.locator('#crumbs').innerText()) && (await owner.page.locator('#crumbs .crumb.current', { hasText: top }).count()) === 1);
check('the subfolder is listed inside it', (await row(owner, sub).count()) === 1);
await owner.page.goBack();
await owner.page.waitForFunction(() => !location.search.includes('folder='));
await row(owner, title).locator('button[title="Move to folder…"]').click();
await owner.page.waitForSelector('#dlg[open] .move-tree');
await owner.page.locator('#dlg .move-item', { hasText: sub }).click();
await owner.page.waitForFunction(() => !document.getElementById('dlg').open);
await row(owner, title).waitFor({ state: 'detached', timeout: 10000 }); // the PUT finished and the list re-rendered
check('after moving, the diagram leaves the root', (await row(owner, title).count()) === 0);
await reload(owner, '?folder=' + id2);
check('a reload on the subfolder URL shows the diagram there', (await row(owner, title).count()) === 1);
check('the API reports the placement', ((await api(owner, 'GET', '/documents')).json.documents.find(d => d.id === doc) || {}).folderId === id2);

// ---- drag and drop: pull it up to the top folder via its breadcrumb
await owner.page.locator('#docs .doc', { hasText: title }).dragTo(owner.page.locator('#crumbs a.crumb', { hasText: top }));
await owner.page.waitForFunction((t) => ![...document.querySelectorAll('#docs .doc')].some(e => e.textContent.includes(t)), title, { timeout: 10000 });
check('dragging onto a breadcrumb files it there', ((await api(owner, 'GET', '/documents')).json.documents.find(d => d.id === doc) || {}).folderId === id1);

// ---- recipient: it arrives in "Shared with me", and the peer files it in a folder of their own
await reload(peer);
check('the peer sees a "Shared with me" folder, and the diagram is not at the root', (await row(peer, 'Shared with me').count()) === 1 && (await row(peer, title).count()) === 0);
await row(peer, 'Shared with me').locator('a.doc-link').click();
await row(peer, title).waitFor();
check('the diagram is inside "Shared with me"', (await row(peer, title).count()) === 1);
const pf = await api(peer, 'POST', '/folders', { name: 'Dari ' + tag });
await reload(peer, '?folder=shared'); // the folder was made through the API: reload so the page knows it
await row(peer, title).waitFor();
await row(peer, title).locator('button[title="Move to folder…"]').click();
await peer.page.waitForSelector('#dlg[open] .move-tree');
await peer.page.locator('#dlg .move-item', { hasText: 'Dari ' + tag }).click();
await peer.page.waitForFunction(() => !document.getElementById('dlg').open);
await row(peer, title).waitFor({ state: 'detached', timeout: 10000 });
await reload(peer, '?folder=' + pf.json.folder.id);
check('the peer filed the shared diagram in their own folder', (await row(peer, title).count()) === 1);
check("the owner's view is untouched", ((await api(owner, 'GET', '/documents')).json.documents.find(d => d.id === doc) || {}).folderId === id1);
check('the peer cannot file into the owner’s folder (404)', (await api(peer, 'PUT', '/documents/' + doc + '/placement', { folderId: id1 })).status === 404);

// ---- search lists flat across folders with the location
await reload(owner);
await owner.page.fill('#filter', title);
await row(owner, title).waitFor();
check('search finds it across folders and shows its location', /Proyek/.test(await row(owner, title).innerText()) && (await owner.page.locator('#crumbs').isHidden()));
await owner.page.fill('#filter', '');

// ---- delete the top folder: contents move up, nothing is lost
await reload(owner);
await row(owner, top).locator('button[title="Delete folder"]').click();
await owner.page.waitForSelector('#dlg[open]');
await owner.page.locator('#dlg-actions .btn.primary').click();
await row(owner, title).waitFor({ timeout: 10000 });
const after = (await api(owner, 'GET', '/documents')).json.documents.find(d => d.id === doc);
check('deleting the folder moves its contents to the parent (root); the diagram still exists', after && !after.folderId && (await row(owner, sub).count()) === 1);
check('the subfolder moved up too', ((await api(owner, 'GET', '/folders')).json.folders.find(f => f.id === id2) || {}).parentId === null);

// ---- cleanup
await api(owner, 'DELETE', '/folders/' + id2);
await api(owner, 'DELETE', '/documents/' + doc);
await api(peer, 'DELETE', '/folders/' + pf.json.folder.id);
await browser.close();
finish();
