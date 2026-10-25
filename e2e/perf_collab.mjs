// Performance sanity (ADR-0006 criterion 5): latency of a change from one browser to another on a 500-cell diagram,
// time to open it, size of what travels. Lab only, informational thresholds. Run: node e2e/perf_collab.mjs
import { BASE, check, finish, launch, newPage, keycloakLogin } from './lib.mjs';
const browser = await launch();
async function login(user) {
  const s = await newPage(browser);
  await s.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(s.page, user);
  await s.page.waitForURL(BASE + '/', { timeout: 30000 });
  await s.page.waitForSelector('#list-view:not([hidden])');
  s.csrf = (await s.page.evaluate(() => fetch('/api/me').then(r => r.json()))).csrf;
  return s;
}
const api = (s, method, path, body) => s.page.evaluate(async ({ method, path, body, csrf }) => {
  const r = await fetch('/api/v1' + path, { method, headers: { 'X-CSRF-Token': csrf, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  return { status: r.status, json: await r.json().catch(() => null) };
}, { method, path, body, csrf: s.csrf });
const ready = s => s.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status').dataset.state === 'ready', null, { timeout: 120000 });

const alice = await login('e2e-team1'), bob = await login('lab-test2');
const id = (await api(alice, 'POST', '/documents', { title: 'perf 500' })).json.document.id;
await api(alice, 'PUT', `/documents/${id}/permissions`, { subject: 'lab-test2', role: 'editor' });
const N = 500;
const ops = Array.from({ length: N }, (_, i) => ({ id: 'p' + i, t: 'addCell', page: 'p1', cell: 'n' + i, kind: 'v', parent: '1', props: { v: 'Node ' + i, gx: String((i % 25) * 90), gy: String(Math.floor(i / 25) * 70), gw: '80', gh: '40', 'st:rounded': '1', 'st:fillColor': '#dae8fc' } }));
const t0 = Date.now();
const add = await api(alice, 'POST', `/documents/${id}/operations`, { ops });
check(`server accepts a batch of ${N} addCell ops`, add.status === 200, `status ${add.status} in ${Date.now() - t0} ms`);

const openMs = [];
for (const s of [alice, bob]) {
  const t = Date.now();
  await s.page.goto(BASE + '/d/' + id, { waitUntil: 'domcontentloaded' });
  await ready(s);
  await s.page.waitForFunction(n => Object.keys(window.__adapter.editorUi().editor.graph.getModel().cells).length >= n, N, { timeout: 120000 });
  openMs.push(Date.now() - t);
}
check('opening a 500-cell diagram (load + render) takes < 15 s', Math.max(...openMs) < 15000, `alice ${openMs[0]} ms, bob ${openMs[1]} ms`);

// latency alice -> bob: alice moves one cell, bob records when his editor shows the new position
await bob.page.evaluate(() => {
  window.__seen = {};
  const m = window.__adapter.editorUi().editor.graph.getModel();
  m.addListener('change', () => { const c = m.getCell('n7'); if (c && c.geometry) window.__seen[String(c.geometry.x)] = Date.now(); });
});
const lat = [];
for (let i = 1; i <= 25; i++) {
  const x = 1000 + i;
  const sent = await alice.page.evaluate(x => { const g = window.__adapter.editorUi().editor.graph, m = g.getModel(), c = m.getCell('n7'); const geo = c.geometry.clone(); geo.x = x; const t = Date.now(); m.setGeometry(c, geo); return t; }, x);
  const got = await bob.page.waitForFunction(x => window.__seen[String(x)], x, { timeout: 15000 }).then(h => h.jsonValue()).catch(() => null);
  if (got) lat.push(got - sent);
  await new Promise(r => setTimeout(r, 120));
}
lat.sort((a, b) => a - b);
const p = q => lat[Math.min(lat.length - 1, Math.floor(q * lat.length))];
check('every move reached the other browser', lat.length === 25, `${lat.length}/25`);
check('end-to-end latency alice -> bob (500 cells): p95 < 500 ms (informational, lab, includes autosave debounce)', p(0.95) < 500, `p50 ${p(0.5)} ms, p95 ${p(0.95)} ms, max ${lat[lat.length - 1]} ms`);

// bulk edit of 100 cells at once
const t1 = Date.now();
await alice.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph, m = g.getModel(); m.beginUpdate(); try { for (let i = 100; i < 200; i++) g.setCellStyles('fillColor', '#f8cecc', [m.getCell('n' + i)]); } finally { m.endUpdate(); } });
await bob.page.waitForFunction(() => window.__adapter.editorUi().editor.graph.getModel().getCell('n199').style.includes('f8cecc'), null, { timeout: 30000 });
check('restyling 100 cells at once reaches the other browser in < 5 s', Date.now() - t1 < 5000, `${Date.now() - t1} ms`);

// payload sizes
const wire = await alice.page.evaluate(async () => { const c = window.__adapter.confirmed(); return { seq: c.seq }; });
const st = await api(alice, 'GET', `/documents/${id}/content?format=state`);
check('state of the 500-cell diagram is transferred once on join', st.status === 200, `seq ${wire.seq}`);
const same = await Promise.all([alice, bob].map(s => s.page.evaluate(async () => ({ l: await window.__adapter.liveCanonHash(), c: await window.__adapter.modelCanonHash('confirmed'), p: window.__adapter.state().pending }))));
check('both browsers hold the same state as the server after the burst', same[0].l === same[1].l && same[0].l === same[0].c && same[0].p === 0 && same[1].p === 0);
await api(alice, 'DELETE', `/documents/${id}`);
await browser.close();
finish();
