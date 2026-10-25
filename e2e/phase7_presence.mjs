// SPIKE-003 / Phase 7 E2E in real Chrome: live presence (selection, editing + draft, drag/resize previews, AI "applying"),
// lifecycle (commit, cancel, disconnect, heartbeat loss), security (viewer, no access), concurrency and latency with 10 clients.
// Presence is ephemeral: the suite also proves it never writes to the operation log. Run: node e2e/phase7_presence.mjs
import { BASE, check, finish, launch, newPage, keycloakLogin, agentToken } from './lib.mjs';

const browser = await launch();
const sleep = ms => new Promise(r => setTimeout(r, ms));
const KC = 'https://auth.lab.local/realms/lab/protocol/openid-connect/token';

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
const api = (s, method, path, body) => s.page.evaluate(async ({ method, path, body, csrf }) => {
  const r = await fetch('/api/v1' + path, { method, credentials: 'same-origin', headers: Object.assign({ 'X-CSRF-Token': csrf }, body !== undefined ? { 'Content-Type': 'application/json' } : {}), body: body !== undefined ? JSON.stringify(body) : undefined });
  const t = await r.text(); let j = null; try { j = JSON.parse(t); } catch (e) { /* text */ }
  return { status: r.status, json: j };
}, { method, path, body, csrf: s.csrf });
async function open(s, id) {
  await s.page.goto(BASE + '/d/' + id, { waitUntil: 'domcontentloaded' });
  await s.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status')?.dataset.state === 'ready' && window.__adapter.presence(), null, { timeout: 90000 });
}
const seq = async (s, id) => (await api(s, 'GET', `/documents/${id}/content?format=state`)).json.seq;
// polls what the browser has actually rendered into the editor for remote collaborators
const rendered = s => s.page.evaluate(() => window.__adapter.presence().rendered());
async function waitRendered(s, pred, ms = 8000) {
  const end = Date.now() + ms; let last = [];
  while (Date.now() < end) { last = await rendered(s); if (pred(last)) return { ok: true, last }; await sleep(100); }
  return { ok: false, last };
}
const has = (list, user, state, cell) => list.some(x => x.user === user && x.state === state && (!cell || x.cell === cell));
const hasNone = (list, user) => !list.some(x => x.user === user);
// screen box of a cell (scrolls it into view first)
const box = (s, cell) => s.page.evaluate(c => {
  const g = window.__adapter.editorUi().editor.graph, m = g.getModel(), cl = m.getCell(c); g.scrollCellToVisible(cl, true);
  const st = g.view.getState(cl), r = g.container.getBoundingClientRect(), f = document.getElementById('editor').getBoundingClientRect();
  return { x: f.left + r.left + st.x - g.container.scrollLeft, y: f.top + r.top + st.y - g.container.scrollTop, w: st.width, h: st.height };
}, cell);
// a point on empty canvas inside the editor frame (used to commit a text edit by clicking away)
const empty = s => s.page.evaluate(() => { const f = document.getElementById('editor').getBoundingClientRect(); return { x: f.left + f.width * 0.75, y: f.top + f.height * 0.85 }; });
const geo = (s, cell) => s.page.evaluate(c => { const g = window.__adapter.editorUi().editor.graph.getModel().getCell(c).geometry; return [g.x, g.y, g.width, g.height]; }, cell);
const label = (s, cell) => s.page.evaluate(c => window.__adapter.editorUi().editor.graph.getModel().getCell(c).value, cell);
const tool = async (ctx, token, name, args) => {
  const r = await ctx.request.post(BASE + '/mcp', { headers: { Authorization: 'Bearer ' + token }, data: { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name, arguments: args } }, failOnStatusCode: false });
  const j = await r.json(); return { ok: !!j.result && !j.result.isError, text: j.result?.content?.[0]?.text || j.error?.message || '' };
};

const alice = await login('e2e-team1'), bob = await login('lab-test2'), carol = await login('e2e-user2');
const ALICE = 'e2e-team1', BOB = 'lab-test2', CAROL = 'e2e-user2';
const id = (await api(alice, 'POST', '/documents', { title: 'Live presence ' + Date.now() })).json.document.id;
await api(alice, 'PUT', `/documents/${id}/permissions`, { subject: BOB, role: 'editor' });
await api(alice, 'PUT', `/documents/${id}/permissions`, { subject: CAROL, role: 'viewer' });
const mk = (cell, label, x, y) => ({ id: 'o' + cell, t: 'addCell', page: 'p1', cell, kind: 'v', parent: '1', props: { v: label, gx: String(x), gy: String(y), gw: '120', gh: '60' } });
await api(alice, 'POST', `/documents/${id}/operations`, { ops: [mk('A', 'Database', -500, -420), mk('B', 'Cache', -250, -420), mk('C', 'Queue', -500, -300)] });
await Promise.all([open(alice, id), open(bob, id), open(carol, id)]);
check('three users open the diagram (owner, editor, viewer) and the presence layer is live', true);

// ---- selection (P0) ------------------------------------------------------------------------------------------------------------------
const seq0 = await seq(alice, id);
await alice.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.setSelectionCell(g.getModel().getCell('A')); });
let r = await waitRendered(bob, l => has(l, ALICE, 'selecting', 'A'));
check('SELECTION: bob sees that alice selected "Database"', r.ok, JSON.stringify(r.last));
check('SELECTION: the viewer sees it too', (await waitRendered(carol, l => has(l, ALICE, 'selecting', 'A'))).ok);
check('SELECTION: alice does not see her own presence', hasNone(await rendered(alice), ALICE));
check('the overlay is a real SVG element inside the editor with the collaborator name', await bob.page.evaluate(() => { const l = window.__adapter.editorUi().editor.graph.view.getOverlayPane().querySelector('[data-presence-layer] [data-presence-user]'); return !!l && /E2E|Team|e2e/i.test(l.textContent + l.getAttribute('data-presence-user')); }));
await alice.page.evaluate(() => window.__adapter.editorUi().editor.graph.clearSelection());
check('deselecting removes it', (await waitRendered(bob, l => hasNone(l, ALICE))).ok);
check('selecting alone wrote nothing to the operation log', (await seq(alice, id)) === seq0, `seq ${seq0} -> ${await seq(alice, id)}`);

// ---- editing + live draft (P0 / P2) -----------------------------------------------------------------------------------------------
let b = await box(alice, 'A');
await alice.page.mouse.dblclick(b.x + b.w / 2, b.y + b.h / 2);
r = await waitRendered(bob, l => has(l, ALICE, 'editing', 'A'));
check('EDITING: bob sees "alice is editing Database" (real double-click in alice\'s browser)', r.ok, JSON.stringify(r.last));
await alice.page.keyboard.type('PostgreSQL', { delay: 40 });
const draftSeen = await bob.page.waitForFunction(() => window.__adapter.presence().list().some(a => a.state === 'editing' && /PostgreSQL/.test(a.draft || '')), null, { timeout: 6000 }).then(() => true).catch(() => false);
check('DRAFT: bob sees the text alice is typing before she commits (level 2)', draftSeen);
check('DRAFT: the committed label is still the old one for bob (draft is not document state)', (await label(bob, 'A')) === 'Database', await label(bob, 'A'));
check('DRAFT: no operation was written while typing', (await seq(alice, id)) === seq0);
check('the draft is drawn next to the shape as plain text (no HTML injection surface)', await bob.page.evaluate(() => [...window.__adapter.editorUi().editor.graph.view.getOverlayPane().querySelectorAll('[data-presence-state="editing"] text')].some(t => /PostgreSQL/.test(t.textContent))));
// commit by clicking on empty canvas
{ const e = await empty(alice); await alice.page.mouse.click(e.x, e.y); }
check('COMMIT: bob receives the committed semantic change', await bob.page.waitForFunction(() => window.__adapter.editorUi().editor.graph.getModel().getCell('A').value === 'PostgreSQL', null, { timeout: 8000 }).then(() => true).catch(() => false));
r = await waitRendered(bob, l => !has(l, ALICE, 'editing'));
check('COMMIT: the editing badge is gone afterwards', r.ok, JSON.stringify(r.last));
const seqCommit = await seq(alice, id);
check('COMMIT: exactly the label change reached the log (1 batch)', seqCommit > seq0 && seqCommit <= seq0 + 2, `seq ${seq0} -> ${seqCommit}`);

// cancel: open the editor and leave without changing anything -> badge disappears, no operation
b = await box(alice, 'B');
await alice.page.mouse.dblclick(b.x + b.w / 2, b.y + b.h / 2);
check('CANCEL: editing badge appears', (await waitRendered(bob, l => has(l, ALICE, 'editing', 'B'))).ok);
await alice.page.keyboard.press('Escape');
check('CANCEL: badge disappears for bob', (await waitRendered(bob, l => !has(l, ALICE, 'editing'))).ok);
await sleep(500);
check('CANCEL: no false committed operation', (await seq(alice, id)) === seqCommit, `seq ${seqCommit} -> ${await seq(alice, id)}`);

// ---- drag preview (P1) -------------------------------------------------------------------------------------------------------------------
b = await box(alice, 'C');
const g0 = await geo(bob, 'C');
await alice.page.mouse.move(b.x + b.w / 2, b.y + b.h / 2);
await alice.page.mouse.down();
for (let i = 1; i <= 10; i++) { await alice.page.mouse.move(b.x + b.w / 2 + i * 12, b.y + b.h / 2 + i * 6); await sleep(70); }
r = await waitRendered(bob, l => has(l, ALICE, 'dragging', 'C'));
check('DRAG: while alice is still dragging, bob sees "alice is moving Queue" with a ghost', r.ok, JSON.stringify(r.last));
const ghost = await bob.page.evaluate(() => { const a = window.__adapter.presence().list().find(x => x.state === 'dragging'); return a && a.preview; });
check('DRAG: the preview carries the live offset (dx>0, dy>0)', ghost && ghost.dx > 40 && ghost.dy > 20, JSON.stringify(ghost));
check('DRAG: nothing is committed during the drag (bob\'s shape and the log are unchanged)', JSON.stringify(await geo(bob, 'C')) === JSON.stringify(g0) && (await seq(alice, id)) === seqCommit);
await alice.page.mouse.up();
check('DRAG: after release the semantic move is committed for bob', await bob.page.waitForFunction(g => { const c = window.__adapter.editorUi().editor.graph.getModel().getCell('C').geometry; return c.x !== g[0] || c.y !== g[1]; }, g0, { timeout: 8000 }).then(() => true).catch(() => false));
check('DRAG: the ghost is gone after release', (await waitRendered(bob, l => !has(l, ALICE, 'dragging'))).ok);

// ---- resize preview (P1) -------------------------------------------------------------------------------------------------------------------
await alice.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.setSelectionCell(g.getModel().getCell('B')); });
await sleep(400);
b = await box(alice, 'B');
const sz0 = await geo(bob, 'B');
await alice.page.mouse.move(b.x + b.w, b.y + b.h);
await alice.page.mouse.down();
for (let i = 1; i <= 8; i++) { await alice.page.mouse.move(b.x + b.w + i * 10, b.y + b.h + i * 6); await sleep(70); }
r = await waitRendered(bob, l => has(l, ALICE, 'resizing', 'B'));
check('RESIZE: bob sees "alice is resizing Cache" while the mouse is still down', r.ok, JSON.stringify(r.last));
const rz = await bob.page.evaluate(() => { const a = window.__adapter.presence().list().find(x => x.state === 'resizing'); return a && a.preview; });
check('RESIZE: the preview carries the live size (wider than the committed 120x60)', rz && rz.w > 130 && rz.h > 70, JSON.stringify(rz));
check('RESIZE: not committed yet', JSON.stringify(await geo(bob, 'B')) === JSON.stringify(sz0));
await alice.page.mouse.up();
check('RESIZE: committed after release', await bob.page.waitForFunction(g => window.__adapter.editorUi().editor.graph.getModel().getCell('B').geometry.width > g[2], sz0, { timeout: 8000 }).then(() => true).catch(() => false));
await alice.page.evaluate(() => window.__adapter.editorUi().editor.graph.clearSelection());

// ---- concurrency: both edit the same shape; presence informs, the operation layer resolves ----------------------------------------------------------------
const bm = await box(bob, 'A'), am = await box(alice, 'A');
await Promise.all([alice.page.mouse.dblclick(am.x + am.w / 2, am.y + am.h / 2), bob.page.mouse.dblclick(bm.x + bm.w / 2, bm.y + bm.h / 2)]);
r = await waitRendered(carol, l => has(l, ALICE, 'editing', 'A') && has(l, BOB, 'editing', 'A'));
check('CONCURRENT: the viewer sees both alice and bob editing the same shape', r.ok, JSON.stringify(r.last));
check('CONCURRENT: each editor sees the other one', (await waitRendered(alice, l => has(l, BOB, 'editing', 'A'))).ok && (await waitRendered(bob, l => has(l, ALICE, 'editing', 'A'))).ok);
await alice.page.keyboard.press('Control+A'); await alice.page.keyboard.type('from alice');
await bob.page.keyboard.press('Control+A'); await bob.page.keyboard.type('from bob');
{ const ea = await empty(alice), eb = await empty(bob); await alice.page.mouse.click(ea.x, ea.y); await bob.page.mouse.click(eb.x, eb.y); }
let la, lb, lc;
for (let i = 0; i < 60; i++) { await sleep(200); [la, lb, lc] = await Promise.all([label(alice, 'A'), label(bob, 'A'), label(carol, 'A')]); if (la === lb && lb === lc && /from (alice|bob)/.test(la)) break; }
check('CONCURRENT: semantic conflict handling is unchanged: one winner, identical everywhere', la === lb && lb === lc && /from (alice|bob)/.test(la), `${la} | ${lb} | ${lc}`);
check('CONCURRENT: presence cleared for both', (await waitRendered(carol, l => !has(l, ALICE, 'editing') && !has(l, BOB, 'editing'))).ok);

// ---- viewer + spoofing ----------------------------------------------------------------------------------------------------------------------------
await carol.page.evaluate(() => window.__adapter.rawAct({ state: 'editing', page: 'p1', cells: ['A'], draft: 'viewer typing' }));
r = await waitRendered(bob, l => l.some(x => x.user === CAROL));
check('VIEWER: a viewer\'s "editing" claim is shown as a plain selection (server enforces the role)', r.ok && has(r.last, CAROL, 'selecting', 'A') && !has(r.last, CAROL, 'editing'), JSON.stringify(r.last));
await carol.page.evaluate(() => window.__adapter.rawAct({ state: 'selecting', page: 'p1', cells: ['A'], user: 'e2e-team1', sid: 'forged', name: 'Alice', type: 'human' }));
await sleep(500);
const lst = await bob.page.evaluate(() => window.__adapter.presence().list().filter(a => a.sid === 'forged' || (a.user === 'e2e-team1' && a.state === 'selecting')));
check('SPOOF: identity fields in a client message are ignored (cannot impersonate alice)', lst.length === 0, JSON.stringify(lst));
await carol.page.evaluate(() => window.__adapter.rawAct({ state: 'idle' }));

// ---- no access --------------------------------------------------------------------------------------------------------------------------------------------
const secret = (await api(alice, 'POST', '/documents', { title: 'private' })).json.document.id;
const wsTry = await carol.page.evaluate(docId => new Promise(res => {
  const ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/ws/v1/documents/' + docId);
  let opened = false; ws.onopen = () => { opened = true; }; ws.onclose = () => res({ opened }); setTimeout(() => res({ opened, timeout: true }), 8000);
}), secret);
check('SECURITY: a user without access to a diagram cannot open its presence channel at all', wsTry.opened === false && !wsTry.timeout, JSON.stringify(wsTry));
await api(alice, 'DELETE', `/documents/${secret}`);

// ---- lifecycle: heartbeat loss and closed browser ------------------------------------------------------------------------------------------------
await alice.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.setSelectionCell(g.getModel().getCell('C')); });
check('LIFECYCLE: alice selects again', (await waitRendered(bob, l => has(l, ALICE, 'selecting', 'C'))).ok);
await alice.page.evaluate(() => window.__adapter.presence().pauseHeartbeat(true));
const tFreeze = Date.now();
r = await waitRendered(bob, l => hasNone(l, ALICE), 14000);
check('LIFECYCLE: a frozen browser (no heartbeat) disappears from the others after the TTL', r.ok && Date.now() - tFreeze > 3000 && Date.now() - tFreeze < 13000, `${Date.now() - tFreeze} ms`);
await alice.page.evaluate(() => window.__adapter.presence().pauseHeartbeat(false));
await alice.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.clearSelection(); g.setSelectionCell(g.getModel().getCell('B')); });
check('LIFECYCLE: presence comes back when the browser wakes up', (await waitRendered(bob, l => has(l, ALICE, 'selecting', 'B'))).ok);
await alice.page.close();
r = await waitRendered(bob, l => hasNone(l, ALICE), 6000);
check('DISCONNECT: closing the browser removes the collaborator immediately (socket close)', r.ok, JSON.stringify(r.last));
const alice2 = await login('e2e-team1'); await open(alice2, id);
await alice2.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.setSelectionCell(g.getModel().getCell('A')); });
check('LATE JOIN: bob sees the new session; a joiner is told what is active right now', (await waitRendered(bob, l => has(l, ALICE, 'selecting', 'A'))).ok);
await bob.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.setSelectionCell(g.getModel().getCell('C')); });
await sleep(400);
await alice2.page.reload({ waitUntil: 'domcontentloaded' });
await alice2.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && window.__adapter.presence(), null, { timeout: 60000 });
check('LATE JOIN: after a reload alice immediately sees bob\'s current selection (snapshot on join)', (await waitRendered(alice2, l => has(l, BOB, 'selecting', 'C'))).ok);
await bob.page.evaluate(() => window.__adapter.editorUi().editor.graph.clearSelection());
await alice2.page.evaluate(() => window.__adapter.editorUi().editor.graph.clearSelection());

// ---- AI presence ----------------------------------------------------------------------------------------------------------------------------------------------
await api(alice2, 'PUT', `/documents/${id}/ai-policy`, { allowed: true, maxOpsPerMin: 120, maxDelete: 5 });
const tokA = await agentToken(alice2.ctx, ALICE);
await tool(alice2.ctx, tokA, 'list_diagrams', {}); // first call registers the AI actor
await api(alice2, 'PUT', `/documents/${id}/permissions`, { subject: 'ai-claude', role: 'editor' });
const seqAi0 = await seq(alice2, id);
const t0 = Date.now();
const ai = await tool(alice2.ctx, tokA, 'add_shape', { document: id, label: 'AI shape', x: -250, y: -300 });
check('AI: the agent applies a change through MCP', ai.ok, ai.text);
const aiCell = /shape (\S+)/.exec(ai.text)?.[1];
r = await waitRendered(bob, l => l.some(x => x.type === 'ai' && x.state === 'applying' && x.cell === aiCell), 6000);
check('AI: people see "AI applying changes" on exactly the cells the batch touched (truthful state, not fake typing)', r.ok, JSON.stringify(r.last));
check('AI: its state is "applying", never "editing"', !(await rendered(bob)).some(x => x.type === 'ai' && x.state === 'editing'));
r = await waitRendered(bob, l => !l.some(x => x.type === 'ai'), 9000);
check('AI: the indicator disappears by itself when the agent stops', r.ok && Date.now() - t0 < 12000, `${Date.now() - t0} ms`);
check('AI: only the real operation reached the log', (await seq(alice2, id)) === seqAi0 + 1, `${seqAi0} -> ${await seq(alice2, id)}`);

// ---- latency with 11 sockets ----------------------------------------------------------------------------------------------------------------------------------------------------------
// Transport latency is measured with sender and receiver in ONE page (a hidden browser tab throttles timers and would distort the clock);
// the other 8 sockets only listen. A second, end-to-end measurement uses the two real browsers (selection -> overlay drawn on the other side).
const seqPerf0 = await seq(alice2, id);
const tr = await alice2.page.evaluate(({ docId }) => new Promise(resolve => {
  const mk = () => new Promise(ok => {
    const w = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/ws/v1/documents/' + docId);
    w.onopen = () => w.send(JSON.stringify({ t: 'join', since: 0 }));
    w.onmessage = e => { if (JSON.parse(e.data).t === 'joined') { w.onmessage = null; ok(w); } };
  });
  Promise.all(Array.from({ length: 9 }, mk)).then(ws => { // 9 here + 2 real browsers' sockets (+ bob's) = 11+ clients on the diagram
    const lat = []; ws[1].onmessage = e => { const m = JSON.parse(e.data); if (m.t === 'act' && m.act.draft) lat.push(Date.now() - Number(m.act.draft)); };
    let k = 0;
    const t = setInterval(() => {
      ws[0].send(JSON.stringify({ t: 'act', act: { state: 'editing', page: 'p1', cells: ['A'], draft: String(Date.now()) } }));
      if (++k >= 80) { clearInterval(t); setTimeout(() => { ws.forEach(w => w.close()); resolve(lat); }, 800); }
    }, 40);
  });
}), { docId: id });
tr.sort((x, y) => x - y);
const pq = q => tr[Math.min(tr.length - 1, Math.floor(q * tr.length))];
check('PERF: presence updates flow with 12 sockets on the diagram (3 real browsers + 9)', tr.length >= 40, `${tr.length} of 80 delivered (the per-session budget drops bursts by design)`);
check('PERF: transport latency p95 < 100 ms in the lab', tr.length > 0 && pq(0.95) < 100, `p50 ${pq(0.5)} ms, p95 ${pq(0.95)} ms, max ${tr[tr.length - 1]} ms`);
check('PERF: the 80 presence updates wrote nothing to PostgreSQL (operation log unchanged)', (await seq(alice2, id)) === seqPerf0);
const e2e = [];
for (let i = 0; i < 8; i++) {
  const cell = ['A', 'B', 'C'][i % 3];
  const t0 = Date.now();
  await alice2.page.evaluate(c => { const g = window.__adapter.editorUi().editor.graph; g.setSelectionCell(g.getModel().getCell(c)); }, cell);
  await bob.page.waitForFunction(c => window.__adapter.presence().rendered().some(x => x.cell === c && x.state === 'selecting'), cell, { polling: 10, timeout: 5000 });
  e2e.push(Date.now() - t0);
}
e2e.sort((x, y) => x - y);
check('PERF: end to end, a selection in one real browser is drawn in the other one within 400 ms (p95, includes the 40 ms client debounce)', e2e[Math.floor(e2e.length * 0.95)] < 400, `${e2e.join(', ')} ms`);

// ---- nothing of this is in history / audit ----------------------------------------------------------------------------------------------------------------------------------------
const hist = (await api(alice2, 'GET', `/documents/${id}/history?limit=100`)).json.history;
check('history contains only real changes (no presence entries)', hist.every(h => h.types.every(t => !/act|presence/i.test(t))) && hist.length < 12, `${hist.length} batches`);
const aud = (await api(alice2, 'GET', `/documents/${id}/audit`)).json.events;
check('audit has no per-keystroke / per-drag records', aud.length < 60 && !aud.some(e => /^act|presence/.test(e.action)), `${aud.length} events`);
check('cleanup', (await api(alice2, 'DELETE', `/documents/${id}`)).status === 200);

await browser.close();
finish();
