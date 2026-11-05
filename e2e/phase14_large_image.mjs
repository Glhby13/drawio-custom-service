// Large pasted images (2026-11-05): draw.io stores a pasted image as a base64 data-URI inside the cell style. Values used to be
// capped at 64 KiB ("Change rejected: bad_prop"); the cap is now 25 MiB end to end (model, WebSocket, REST, gateway, ingress).
// Also exercises replica fan-out: two users are likely served by different platform pods.
// Run: node e2e/phase14_large_image.mjs
import { BASE, check, finish, launch, newPage, keycloakLogin, kubectl } from './lib.mjs';

const browser = await launch();
const sleep = ms => new Promise(r => setTimeout(r, ms));

async function login(user) {
  const s = await newPage(browser);
  await s.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(s.page, user);
  await s.page.waitForURL(BASE + '/', { timeout: 30000 });
  await s.page.waitForSelector('#list-view:not([hidden])', { timeout: 20000 });
  s.csrf = (await s.page.evaluate(() => fetch('/api/me', { credentials: 'same-origin' }).then(r => r.json()))).csrf;
  return s;
}
function api(s, method, path, body) {
  return s.page.evaluate(async ({ method, path, body, csrf }) => {
    const r = await fetch('/api/v1' + path, { method, credentials: 'same-origin', headers: Object.assign({ 'X-CSRF-Token': csrf }, body !== undefined ? { 'Content-Type': 'application/json' } : {}), body: body !== undefined ? JSON.stringify(body) : undefined });
    const t = await r.text(); let j = null; try { j = JSON.parse(t); } catch (e) { /* text */ }
    return { status: r.status, json: j, text: t };
  }, { method, path, body, csrf: s.csrf });
}
async function open(s, id) {
  await s.page.goto(BASE + '/d/' + id, { waitUntil: 'domcontentloaded' });
  await s.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status')?.dataset.state === 'ready' && window.__adapter.editorUi(), null, { timeout: 60000 });
}
// a cell whose style carries `bytes` bytes of base64 image data
const addImage = (s, cell, bytes) => s.page.evaluate(([cell, bytes]) => {
  const g = window.__adapter.editorUi().editor.graph;
  let b64 = 'iVBORw0KGgo'; const chunk = 'QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ejAxMjM0NTY3ODk';
  const parts = [b64]; let n = b64.length; while (n < bytes) { parts.push(chunk); n += chunk.length; }
  const data = parts.join('').slice(0, bytes);
  g.getModel().beginUpdate();
  try { g.insertVertex(g.getDefaultParent(), cell, '', 40, 40, 200, 120, 'shape=image;imageAspect=0;image=data:image/png,' + data + ';'); } finally { g.getModel().endUpdate(); }
}, [cell, bytes]);
const styleLen = (s, cell) => s.page.evaluate(c => { const x = window.__adapter.editorUi().editor.graph.getModel().getCell(c); return x ? (x.style || '').length : -1; }, cell);
const waitStyle = (s, cell, min, ms = 60000) => s.page.waitForFunction(([c, min]) => { const x = window.__adapter.editorUi().editor.graph.getModel().getCell(c); return !!x && (x.style || '').length >= min; }, [cell, min], { timeout: ms }).then(() => true).catch(() => false);
const pending = s => s.page.evaluate(() => window.__adapter.state().pending);

const alice = await login('e2e-team1'), bob = await login('lab-test2');
const stamp = new Date().toISOString().slice(11, 19);
const doc = (await api(alice, 'POST', '/documents', { title: 'Large image ' + stamp })).json.document.id;
await api(alice, 'PUT', '/documents/' + doc + '/permissions', { subject: 'lab-test2', role: 'editor' });
await open(alice, doc); await open(bob, doc);

const pods = kubectl('-n lab-dev get pods -l app.kubernetes.io/name=drawio-platform --no-headers').trim().split('\n').filter(Boolean);
check('platform runs more than one replica (fan-out is across pods)', pods.length >= 2, pods.length + ' pods');

for (const [cell, mb] of [['img1', 0.1], ['img2', 1], ['img3', 8], ['img4', 20]]) {
  const bytes = Math.floor(mb * 1024 * 1024);
  await addImage(alice, cell, bytes);
  check('alice pastes a ' + mb + ' MB image: the server accepts it (nothing pending)', await alice.page.waitForFunction(() => window.__adapter.state().pending === 0, null, { timeout: 90000 }).then(() => true).catch(() => false), 'pending=' + await pending(alice));
  check('bob receives the ' + mb + ' MB image', await waitStyle(bob, cell, bytes), 'len=' + await styleLen(bob, cell));
}

// a value above the cap is refused and does not break the session
await addImage(alice, 'toobig', 26 * 1024 * 1024);
await sleep(8000);
check('a 26 MB image is rejected: bob never sees it', (await styleLen(bob, 'toobig')) === -1);
const hist = await api(alice, 'GET', '/documents/' + doc + '/history');
check('the session still works after the rejection', hist.status === 200);

// persistence: reload and read the 20 MB image back from the server
await open(alice, doc);
check('the 20 MB image survives a reload', await waitStyle(alice, 'img4', 20 * 1024 * 1024 - 8, 90000), 'len=' + await styleLen(alice, 'img4'));

await api(alice, 'DELETE', '/documents/' + doc);
await browser.close();
finish();
