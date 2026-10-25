// Phases 3-5 E2E in real Chrome against the lab: documents, sharing/permissions, realtime collaboration between three
// users, viewer read-only, revoke, history/revert, versions/restore, export, reload persistence, pod restart.
// Run: node e2e/phase5_collab.mjs
import { BASE, check, finish, launch, newPage, keycloakLogin, kubectl } from './lib.mjs';

const browser = await launch();
const sleep = ms => new Promise(r => setTimeout(r, ms));

async function login(user) {
  const s = await newPage(browser);
  await s.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(s.page, user);
  await s.page.waitForURL(BASE + '/', { timeout: 30000 });
  await s.page.waitForSelector('#list-view:not([hidden])', { timeout: 20000 });
  s.user = user;
  s.csrf = (await s.page.evaluate(() => fetch('/api/me', { credentials: 'same-origin' }).then(r => r.json()))).csrf;
  return s;
}
// REST through the page (same-origin, session cookie, CSRF header) exactly like the UI does
function api(s, method, path, body) {
  return s.page.evaluate(async ({ method, path, body, csrf }) => {
    const r = await fetch('/api/v1' + path, { method, credentials: 'same-origin', headers: Object.assign({ 'X-CSRF-Token': csrf }, body !== undefined ? { 'Content-Type': 'application/json' } : {}), body: body !== undefined ? JSON.stringify(body) : undefined });
    const t = await r.text(); let j = null; try { j = JSON.parse(t); } catch (e) { /* text */ }
    return { status: r.status, json: j, text: t };
  }, { method, path, body, csrf: s.csrf });
}
async function open(s, id) {
  await s.page.goto(BASE + '/d/' + id, { waitUntil: 'domcontentloaded' });
  await s.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status')?.dataset.state === 'ready' && window.__adapter.editorUi(), null, { timeout: 90000 });
}
const labels = s => s.page.evaluate(() => {
  const ui = window.__adapter.editorUi(), m = ui.editor.graph.getModel(), out = [];
  Object.values(m.cells).forEach(c => { if (c.vertex || c.edge) out.push((typeof c.value === 'string' ? c.value : c.value && c.value.getAttribute && c.value.getAttribute('label')) || ''); });
  return out.sort();
});
const waitLabels = (s, want, ms = 15000) => s.page.waitForFunction(w => {
  const ui = window.__adapter.editorUi(), m = ui.editor.graph.getModel(), out = [];
  Object.values(m.cells).forEach(c => { if (c.vertex || c.edge) out.push((typeof c.value === 'string' ? c.value : c.value && c.value.getAttribute && c.value.getAttribute('label')) || ''); });
  return JSON.stringify(out.sort()) === JSON.stringify(w);
}, want, { timeout: ms }).then(() => true).catch(() => false);
const edit = (s, fn, arg) => s.page.evaluate(([src, arg]) => { const ui = window.__adapter.editorUi(), g = ui.editor.graph; return (new Function('ui', 'g', 'arg', src))(ui, g, arg); }, [fn, arg]);
const settle = s => s.page.evaluate(() => window.__adapter.flush());
async function converged(sessions, ms = 20000) {
  const end = Date.now() + ms; let last = [];
  while (Date.now() < end) {
    last = await Promise.all(sessions.map(s => s.page.evaluate(async () => ({ live: await window.__adapter.liveCanonHash(), conf: await window.__adapter.modelCanonHash('confirmed'), pred: await window.__adapter.modelCanonHash('predicted'), st: window.__adapter.state() }))));
    if (last.every(x => x.live === last[0].live && x.conf === last[0].conf && x.pred === last[0].pred && x.live === x.conf && x.st.pending === 0)) return { ok: true, last };
    await sleep(500);
  }
  return { ok: false, last };
}

const alice = await login('e2e-team1'), bob = await login('lab-test2'), carol = await login('e2e-user2');
const dave = await newPage(browser); // lab-test3: signed in to Keycloak but NOT in the drawio-users group
await dave.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
await keycloakLogin(dave.page, 'lab-test3');
await sleep(4000);
check('user without the application role cannot use the application', !(await dave.page.$('#list-view:not([hidden])')), dave.page.url());
await dave.ctx.close();

// ---- documents + sharing --------------------------------------------------------------------------------------------
const created = await api(alice, 'POST', '/documents', { title: 'E2E collaboration ' + Date.now() });
const id = created.json?.document?.id;
check('owner creates a document (blank, seq 0)', created.status === 201 && id && created.json.role === 'owner', `status ${created.status} id ${id}`);
check('stranger cannot see the document (404, no existence oracle)', (await api(bob, 'GET', '/documents/' + id)).status === 404 && (await api(carol, 'GET', '/documents/' + id + '/content')).status === 404);
check('non-owner cannot share it', (await api(bob, 'PUT', '/documents/' + id + '/permissions', { subject: 'lab-test2', role: 'editor' })).status === 404);
check('owner shares: bob = editor', (await api(alice, 'PUT', '/documents/' + id + '/permissions', { subject: 'lab-test2', role: 'editor' })).status === 200);
check('owner shares: carol = viewer', (await api(alice, 'PUT', '/documents/' + id + '/permissions', { subject: 'e2e-user2', role: 'viewer' })).status === 200);
check('viewer cannot share', (await api(carol, 'PUT', '/documents/' + id + '/permissions', { subject: 'e2e-user2', role: 'editor' })).status === 403);
const list = await api(bob, 'GET', '/documents');
check('shared document appears in the editor\'s list with their role', list.json.documents.some(d => d.id === id && d.role === 'editor'));

// ---- realtime collaboration -----------------------------------------------------------------------------------------
await Promise.all([open(alice, id), open(bob, id), open(carol, id)]);
check('three users open the same diagram (editor, editor, viewer)', true);
const roles = await Promise.all([alice, bob, carol].map(s => s.page.evaluate(() => window.__adapter.state().role)));
check('roles reported by the server', roles.join() === 'owner,editor,viewer', roles.join());
check('viewer editor is read-only (graph disabled)', await carol.page.evaluate(() => !window.__adapter.editorUi().editor.graph.isEnabled()));
check('editor graph is enabled', await bob.page.evaluate(() => window.__adapter.editorUi().editor.graph.isEnabled()));

await edit(alice, `g.getModel().beginUpdate(); try { g.insertVertex(g.getDefaultParent(), 'a1', 'from alice', 40, 40, 120, 60); } finally { g.getModel().endUpdate(); }`);
check('alice\'s shape appears for bob', await waitLabels(bob, ['from alice']));
check('alice\'s shape appears for the viewer', await waitLabels(carol, ['from alice']));
await edit(bob, `g.getModel().beginUpdate(); try { g.insertVertex(g.getDefaultParent(), 'b1', 'from bob', 240, 40, 120, 60, 'rounded=1;fillColor=#dae8fc;'); } finally { g.getModel().endUpdate(); }`);
check('bob\'s shape appears for alice and the viewer', (await waitLabels(alice, ['from alice', 'from bob'])) && (await waitLabels(carol, ['from alice', 'from bob'])));
await edit(alice, `g.getModel().beginUpdate(); try { g.insertEdge(g.getDefaultParent(), 'e1', 'link', g.getModel().getCell('a1'), g.getModel().getCell('b1')); } finally { g.getModel().endUpdate(); }`);
check('an edge between both users\' shapes replicates', await waitLabels(bob, ['from alice', 'from bob', 'link']));
await edit(bob, `g.moveCells([g.getModel().getCell('a1')], 100, 50);`);
await sleep(1500);
const moved = await alice.page.evaluate(() => { const c = window.__adapter.editorUi().editor.graph.getModel().getCell('a1'); return [c.geometry.x, c.geometry.y]; });
check('bob moving alice\'s shape moves it for alice', moved[0] === 140 && moved[1] === 90, JSON.stringify(moved));

// simultaneous edits of the same label and of different cells
await Promise.all([
  edit(alice, `g.getModel().setValue(g.getModel().getCell('b1'), 'alice wins?'); g.getModel().setValue(g.getModel().getCell('a1'), 'alice-only');`),
  edit(bob, `g.getModel().setValue(g.getModel().getCell('b1'), 'bob wins?'); g.setCellStyles('fillColor', '#f8cecc', [g.getModel().getCell('a1')]);`),
]);
const c1 = await converged([alice, bob, carol]);
check('concurrent edits converge on every client (live == confirmed == predicted, nothing pending)', c1.ok, JSON.stringify(c1.last.map(x => x.st)));
const l1 = await labels(alice);
check('same-field conflict resolved to exactly one winner for all', (await labels(bob)).join() === l1.join() && (await labels(carol)).join() === l1.join() && (l1.includes('alice wins?') !== l1.includes('bob wins?')), l1.join(' / '));

// delete vs edit, group, z-order, pages
await Promise.all([
  edit(alice, `g.removeCells([g.getModel().getCell('e1')], true);`),
  edit(bob, `g.getModel().setValue(g.getModel().getCell('e1'), 'edited edge');`),
]);
check('delete vs edit of the same edge converges', (await converged([alice, bob, carol])).ok);
await edit(bob, `const a = g.getModel().getCell('a1'), b = g.getModel().getCell('b1'); g.groupCells(null, 10, [a, b]);`);
check('grouping converges', (await converged([alice, bob, carol])).ok);
await edit(alice, `const ui2 = ui; const p = ui2.createPage('Second', ui2.createPageId()); ui2.insertPage(p);`);
check('a new page created by alice appears for bob and the viewer', await Promise.all([bob, carol].map(s => s.page.waitForFunction(() => window.__adapter.editorUi().pages.length === 2, null, { timeout: 15000 }).then(() => true).catch(() => false))).then(r => r.every(Boolean)));
check('state converges after page creation', (await converged([alice, bob, carol])).ok);
await edit(alice, `ui.selectPage(ui.pages[0]);`); // alice created and switched to page 2; the label helpers read the current page
await sleep(800);

// viewer cannot write, even by calling the API directly
const sneaky = await api(carol, 'POST', '/documents/' + id + '/operations', { batch: 'x1', base: 0, ops: [{ id: 'z1', t: 'setProp', page: 'p1', cell: 'a1', key: 'v', val: 'hacked' }] });
check('viewer write attempt is refused by the server (403)', sneaky.status === 403, 'status ' + sneaky.status);
check('viewer cannot comment as viewer? (viewer may not) -> refused', (await api(carol, 'POST', '/documents/' + id + '/comments', { body: 'hi' })).status === 403);

// presence
const pres = await alice.page.waitForFunction(() => document.querySelectorAll('#presence .pav').length >= 3, null, { timeout: 15000 }).then(() => true).catch(() => false);
check('presence shows the three collaborators', pres, await alice.page.evaluate(() => [...document.querySelectorAll('#presence .pav')].map(x => x.dataset.user).join()));

// ---- history / revert / versions ------------------------------------------------------------------------------------
const hist = await api(alice, 'GET', '/documents/' + id + '/history?limit=100');
check('history lists attributed batches (who, types)', hist.json.history.length > 3 && hist.json.history.every(h => h.actor && h.types.length), `${hist.json.history.length} batches, actors ${[...new Set(hist.json.history.map(h => h.actor))]}`);
const v1 = await api(alice, 'POST', '/documents/' + id + '/versions', { name: 'before cleanup' });
check('named version saved', v1.status === 201, 'status ' + v1.status);
const before = await labels(alice);
await edit(bob, `g.getModel().setValue(g.getModel().getCell('b1'), 'to be reverted');`);
await converged([alice, bob]);
const latest = (await api(alice, 'GET', '/documents/' + id + '/history?limit=1')).json.history[0];
const rv = await api(alice, 'POST', '/documents/' + id + '/revert', { batch: latest.batch });
check('owner reverts bob\'s last change', rv.status === 200 && rv.json.applied >= 1, JSON.stringify(rv.json));
const cv = await converged([alice, bob, carol]);
check('revert reaches every client', cv.ok && (await labels(carol)).join() === before.join(), `converged=${cv.ok} carol=${(await labels(carol)).join('/')} before=${before.join('/')} ${JSON.stringify(cv.last.map(x => x.st))}`);
await edit(alice, `g.removeCells([g.getModel().getCell('b1')], true);`);
await converged([alice, bob]);
const rs = await api(alice, 'POST', '/documents/' + id + '/versions/' + v1.json.version.id + '/restore', {});
check('restore of a named version succeeds', rs.status === 200, JSON.stringify(rs.json));
check('restore reaches every client (resync) and matches the saved state', await waitLabels(bob, before, 25000) && await waitLabels(carol, before, 25000), (await labels(bob)).join());
check('clients converge after restore', (await converged([alice, bob, carol], 30000)).ok);

// ---- export + persistence ---------------------------------------------------------------------------------------------
const xml = await api(bob, 'GET', '/documents/' + id + '/content?format=xml');
check('export as .drawio XML contains the content', xml.status === 200 && xml.text.includes('<mxfile') && before.every(l => !l || xml.text.includes(l.replace(/&/g, '&amp;'))), `len ${xml.text.length}`);
const svg = await api(bob, 'GET', '/documents/' + id + '/content?format=svg');
check('export as SVG', svg.status === 200 && svg.text.includes('<svg'));
await alice.page.reload({ waitUntil: 'domcontentloaded' });
await alice.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status')?.dataset.state === 'ready', null, { timeout: 60000 });
check('reload restores the diagram from the server', await waitLabels(alice, before));

// ---- pod restart: no PVC, state survives; clients reconnect --------------------------------------------------------------
kubectl('-n lab-dev rollout restart deployment/drawio-platform');
kubectl('-n lab-dev rollout status deployment/drawio-platform --timeout=180s');
await edit(bob, `g.getModel().beginUpdate(); try { g.insertVertex(g.getDefaultParent(), 'after-restart', 'after restart', 40, 200, 100, 40); } finally { g.getModel().endUpdate(); }`);
const after = [...before, 'after restart'].sort();
check('after the platform pod restarted, clients reconnect and keep collaborating', await waitLabels(alice, after, 60000) && await waitLabels(carol, after, 60000));
check('clients converge after restart', (await converged([alice, bob, carol], 30000)).ok);

// ---- revoke ----------------------------------------------------------------------------------------------------------
await api(alice, 'PUT', '/documents/' + id + '/permissions', { subject: 'lab-test2', role: '' });
const banner = await bob.page.waitForFunction(() => !document.getElementById('banner').hidden, null, { timeout: 20000 }).then(() => true).catch(() => false);
check('revoking bob disconnects his session with a message', banner, await bob.page.evaluate(() => document.getElementById('banner').textContent));
check('revoked user gets 404 from REST', (await api(bob, 'GET', '/documents/' + id)).status === 404);

// ---- admin: can read/export, cannot edit; delete -----------------------------------------------------------------------
const admin = await login('e2e-drawio-admin');
check('application admin can read any document', (await api(admin, 'GET', '/documents/' + id)).status === 200);
const adminOps = await api(admin, 'POST', '/documents/' + id + '/operations', { batch: 'adm1', base: 0, ops: [{ id: 'zz', t: 'setProp', page: 'p1', cell: 'a1', key: 'v', val: 'x' }] });
check('application admin cannot edit content', adminOps.status === 403, 'status ' + adminOps.status);
const aud = await api(alice, 'GET', '/documents/' + id + '/audit');
check('audit trail records joins and denials', aud.status === 200 && aud.json.events.some(e => e.action === 'ws.join') && aud.json.events.some(e => e.outcome === 'denied'), `${aud.json?.events?.length} events`);
check('owner deletes the document', (await api(alice, 'DELETE', '/documents/' + id)).status === 200 && (await api(alice, 'GET', '/documents/' + id)).status === 404);

await browser.close();
finish();
