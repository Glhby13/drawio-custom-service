// massive-collab.mjs — bounded 2/3-user real-Chrome Collaboration Undo/Redo stress test.
// Author / Agent: Claude Code (test sub-agent) · 2026-11-14
// Scenarios (each at wsDelay 0ms and 250ms):
//   S1 3-user concurrent edits + interleaved rapid Ctrl+Z / toolbar Undo / Redo bursts
//   S2 delete-then-undo (container + 2 children + edge) under active collaboration
//   S3 true conflict: Alice edits X & Y, Bob deletes X, Alice undoes across her history (paced + rapid)
//   S4 multi-page delete + undo (+ redo/undo cycle) while Bob edits page 1
//   S5 WS drop: adapter.resync('test_drop') and a real offline gap (routeWebSocket proxy), then edits + undo/redo
// Uses current production web files and Go model through a local test coordinator.
// Maintainer / Agent: Codex, 2026-11-15: strict semantic assertions and conflict recovery oracle.
import { createRequire } from 'node:module';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { startServer } from './repro-server.mjs';
import { checkCollab } from './collab-checks.mjs';

const require = createRequire(new URL('../package.json', import.meta.url));
const { chromium } = require('playwright-core');
const M = require('../internal/edge/web/model.js');

const HERE = path.dirname(fileURLToPath(import.meta.url));
const OUT = process.env.OUT || path.join(HERE, 'massive-collab.json');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const DELAYS = (process.env.DELAYS || '0,250').split(',').map(Number);
let wsDelay = 0;
const R = { s1: [], s2: [], s3: [], s4: [], s5: [], errors: [], summary: [] };
const LOGS = [];
const { server, port, docs } = await startServer(0, { wsDelayMs: () => wsDelay });
const browser = await chromium.launch({
  executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
});

// ---------------------------------------------------------------- in-page hooks
function installHooks() {
  const ui = window.__adapter.editorUi(), um = ui.editor.undoManager;
  const D = (window.__deep = { undo: [], redo: [], throws: 0, mismatch: 0 });
  for (const name of ['undo', 'redo']) {
    const orig = um[name];
    um[name] = function () {
      const rec = { bIdx: um.indexOfNextAdd, bLen: um.history.length };
      try { return orig.apply(this, arguments); }
      catch (e) { D.throws++; rec.err = String(e).slice(0, 100); throw e; }
      finally { rec.aIdx = um.indexOfNextAdd; rec.aLen = um.history.length; D[name].push(rec); }
    };
  }
  const B = window.DrawioBridge, oDiff = B.diff;
  B.diff = function () { const r = oDiff.apply(this, arguments); if (r.mismatch) D.mismatch++; return r; };
}

function installEditor() {
  window.__k = window.__k || 0;
  const G = () => window.__adapter.editorUi().editor.graph;
  window.__edit = function (kind, own) {
    const g = G(), p = g.getDefaultParent();
    const mine = window.__mine || (window.__mine = new Set());
    let vs = g.getChildVertices(p), es = g.getChildEdges(p);
    if (own) { vs = vs.filter((v) => mine.has(v.id)); es = es.filter((e) => mine.has(e.id)); }
    const pick = (arr, i) => arr[((i % arr.length) + arr.length) % arr.length];
    const k = ++window.__k;
    if (!vs.length && kind !== 'add') kind = 'add';
    if (vs.length < 2 && kind === 'edge') kind = 'add';
    if (!es.length && kind === 'delEdge') kind = 'add';
    const x = 40 + (k % 6) * 140, y = 40 + (Math.floor(k / 6) % 8) * 100;
    switch (kind) {
      case 'add': { const id = g.insertVertex(p, null, 'N' + k, x, y, 110, 50, 'rounded=1;whiteSpace=wrap;html=1;').id; mine.add(id); return id; }
      case 'edge': { const id = g.insertEdge(p, null, 'e' + k, pick(vs, k), pick(vs, k + 1)).id; mine.add(id); return id; }
      case 'move': g.moveCells([pick(vs, k)], 20, 10); return 'move';
      case 'resize': { const v = pick(vs, k + 2); const geo = v.geometry.clone(); geo.width += 20; geo.height += 10; g.resizeCell(v, geo); return 'resize'; }
      case 'label': g.cellLabelChanged(pick(vs, k + 1), 'L' + k); return 'label';
      case 'fill': g.setCellStyles('fillColor', '#' + ['ff8800', '00aa55', '3366ff'][k % 3], [pick(vs, k + 3)]); return 'fill';
      case 'stroke': g.setCellStyles('strokeColor', '#aa0000', [pick(vs, k)]); return 'stroke';
      case 'delEdge': g.removeCells([pick(es, k)]); return 'delEdge';
      case 'delVertex': g.removeCells([pick(vs, k)]); return 'delVertex';
    }
    throw new Error('unknown edit ' + kind);
  };
  // id-addressed helpers
  window.__add = (label, x, y, w, h, style, parentId) => {
    const g = G(), par = parentId ? g.getModel().getCell(parentId) : g.getDefaultParent();
    return g.insertVertex(par, null, label, x, y, w || 110, h || 50, style || 'rounded=1;whiteSpace=wrap;html=1;').id;
  };
  window.__addEdge = (label, a, b) => { const g = G(), m = g.getModel(); return g.insertEdge(g.getDefaultParent(), null, label, m.getCell(a), m.getCell(b)).id; };
  window.__fill = (id, c) => { const g = G(); g.setCellStyles('fillColor', c, [g.getModel().getCell(id)]); };
  window.__label = (id, t) => { const g = G(); g.cellLabelChanged(g.getModel().getCell(id), t); };
  window.__move = (id, dx, dy) => { const g = G(); g.moveCells([g.getModel().getCell(id)], dx, dy); };
  window.__del = (ids) => { const g = G(), m = g.getModel(); g.removeCells(ids.map((i) => m.getCell(i)).filter(Boolean), true); };
  window.__cells = (ids) => {
    const g = G(), m = g.getModel();
    return ids.map((id) => {
      const c = m.getCell(id);
      if (!c) return null;
      const v = typeof c.value === 'string' ? c.value : (c.value && c.value.getAttribute ? c.value.getAttribute('label') : '');
      const fill = (/fillColor=([^;]*)/.exec(c.style || '') || [])[1] || '';
      return { id, parent: c.parent && c.parent.id, label: v, fill, x: c.geometry ? c.geometry.x : null, inModel: m.contains(c) };
    });
  };
  window.__pages = () => {
    const ui = window.__adapter.editorUi(), C = window.__adapter.confirmed();
    return {
      ui: ui.pages.map((p) => p.getName()),
      cur: ui.currentPage && ui.currentPage.getName(),
      confirmed: Array.from(C.pages.values()).map((p) => p.name + ':' + Array.from(p.cells.values()).filter((c) => c.kind === 'v' || c.kind === 'e').length),
    };
  };
}

// ---------------------------------------------------------------- client harness
async function openDoc(docId, user) {
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  const logs = [];
  const net = { blocked: false, client: null, server: null, connects: 0, refused: 0 };
  await page.routeWebSocket(/\/ws\/v1\/documents\//, (ws) => {
    net.connects++;
    if (net.blocked) { net.refused++; ws.close({ code: 4002, reason: 'test offline' }); return; }
    net.client = ws;
    net.server = ws.connectToServer();
  });
  page.on('console', (m) => {
    if (m.type() === 'warning' || m.type() === 'error') {
      const t = m.text();
      if (!/404|Failed to load resource/.test(t)) logs.push(`[${docId}/${user}][${m.type()}] ${t.slice(0, 160)}`);
    }
  });
  const perr = [];
  page.on('pageerror', (e) => { perr.push(String(e)); logs.push(`[${docId}/${user}][pageerror] ${String(e).slice(0, 160)}`); });
  await page.goto(`http://127.0.0.1:${port}/d/${docId}?user=${user}`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && window.__adapter.editorUi(), null, { timeout: 30000 });
  const frame = page.frameLocator('#editor');
  await frame.locator('.geSidebarContainer .geItem').first().waitFor({ timeout: 20000 });
  await page.evaluate(installHooks);
  await page.evaluate(installEditor);
  const isMac = await page.evaluate(() => document.getElementById('editor').contentWindow.mxClient.IS_MAC);
  return {
    ctx, page, frame, logs, perr, net, docId, user,
    undoKey: isMac ? 'Meta+z' : 'Control+z', redoKey: isMac ? 'Meta+Shift+z' : 'Control+y',
  };
}

const ev = (s, fn, arg) => s.page.evaluate(fn, arg);
async function serverCanon(docId) { return M.sha256hex(M.canon(docs.get(docId).doc, { ignoreOrder: true })); }
const srvNacks = (docId) => docs.get(docId)?.nacks.length ?? 0;

async function snap(s) {
  return s.page.evaluate(async () => {
    const a = window.__adapter, ui = a.editorUi(), um = ui.editor.undoManager, g = ui.editor.graph;
    const st = a.state();
    return {
      lh: await a.liveCanonHash(), ch: await a.modelCanonHash('confirmed'),
      nextAdd: um.indexOfNextAdd, histLen: um.history.length,
      cells: Object.values(g.getModel().cells).filter((c) => c.vertex || c.edge).length,
      patches: a.stats.patches, nacked: a.stats.nacked, resyncs: a.stats.resyncs, hashBad: a.stats.hashBad,
      pending: st.pending, busy: st.busy, joined: st.joined,
      mismatch: window.__deep.mismatch, throws: window.__deep.throws,
      undoCalls: window.__deep.undo.length, redoCalls: window.__deep.redo.length,
      undoErrs: window.__deep.undo.concat(window.__deep.redo).filter((r) => r.err).map((r) => r.err),
    };
  });
}

async function waitSettled(s, ms = 8000) {
  await sleep(60);
  const end = Date.now() + ms;
  while (Date.now() < end) {
    const st = await s.page.evaluate(() => window.__adapter.state());
    if (st.joined && st.pending === 0 && !st.busy) return true;
    await sleep(40);
  }
  return false;
}
async function settleAll(cs, ms) { for (let i = 0; i < 2; i++) for (const c of cs) await waitSettled(c, ms); }

// all clients: live == confirmed == server, identical across clients
async function converge(cs, docId, timeout = 20000) {
  const t0 = Date.now();
  let last;
  while (Date.now() - t0 < timeout) {
    await settleAll(cs, 4000);
    const ss = [];
    for (const c of cs) ss.push(await snap(c));
    const srv = await serverCanon(docId);
    const ok = ss.every((x) => x.lh === srv && x.ch === srv && x.pending === 0 && !x.busy && x.joined);
    last = { ok, ms: Date.now() - t0, server: srv.slice(0, 10), clients: ss.map((x, i) => `${cs[i].user}:lh=${x.lh.slice(0, 10)},ch=${x.ch.slice(0, 10)}`) };
    if (ok) return last;
    await sleep(250);
  }
  // diagnostics: what differs between each stuck client's live editor and its confirmed model, and does a manual flush heal it?
  last.stuck = [];
  for (const c of cs) {
    const info = await c.page.evaluate(async () => {
      const a = window.__adapter, Bd = window.DrawioBridge;
      const lh = await a.liveCanonHash(), ch = await a.modelCanonHash('confirmed');
      if (lh === ch) return null;
      const d = Bd.diff(a.confirmed(), a.live());
      const um = a.editorUi().editor.undoManager;
      return { ops: d.ops.slice(0, 6).map((o) => JSON.stringify(o).slice(0, 140)), nOps: d.ops.length, mismatch: !!d.mismatch, nextAdd: um.indexOfNextAdd, histLen: um.history.length, state: a.state() };
    });
    if (!info) continue;
    await c.page.evaluate(() => window.__adapter.flush());
    await settleAll(cs, 4000); await sleep(400); await settleAll(cs, 4000);
    const srv = await serverCanon(docId);
    const after = await c.page.evaluate(async () => [await window.__adapter.liveCanonHash(), await window.__adapter.modelCanonHash('confirmed')]);
    info.flushHealed = after[0] === after[1] && after[1] === srv;
    last.stuck.push({ user: c.user, ...info });
  }
  return last;
}

async function totals(cs, docId) {
  const ss = [];
  for (const c of cs) ss.push(await snap(c));
  return {
    serverNacks: srvNacks(docId),
    allNackCodes: docs.get(docId).nacks.map(n => n.code),
    allDeleteConflictsExplained: docs.get(docId).nacks.every(n => n.explainedDeleteConflict),
    nackCodes: [...new Set(docs.get(docId).nacks.map((n) => `${n.code}:${String(n.msg || '').slice(0, 60)}`))].slice(0, 4),
    throws: ss.reduce((a, x) => a + x.throws, 0),
    mismatch: ss.reduce((a, x) => a + x.mismatch, 0),
    resyncs: ss.reduce((a, x) => a + x.resyncs, 0),
    hashBad: ss.reduce((a, x) => a + x.hashBad, 0),
    clientNacked: ss.reduce((a, x) => a + x.nacked, 0),
    pageErrors: cs.reduce((a, c) => a + c.perr.length, 0),
    undoErrs: ss.flatMap((x) => x.undoErrs).slice(0, 3),
    nackDetail: await Promise.all(docs.get(docId).nacks.slice(0, 8).map(async (n) => {
      const who = (await Promise.all(cs.map(async (c) => ((await c.page.evaluate(() => window.__adapter.state().you)) === n.sid ? c.user : null)))).find(Boolean) || n.sid;
      const op = (n.ops || [])[n.index ?? 0] || {};
      return `${who} code=${n.code} idx=${n.index} nOps=${(n.ops || []).length} op=${JSON.stringify(op).slice(0, 170)}`;
    })),
  };
}

async function focusEditor(s) { await s.page.mouse.click(700, 780); await sleep(40); }
async function toolbar(s, which) {
  try { await s.frame.locator(`[title^="${which}"]:visible`).first().click({ timeout: 1500 }); return 'toolbar'; }
  catch { await ev(s, (w) => window.__adapter.editorUi().actions.get(w).funct(), which.toLowerCase()); return 'fallback'; }
}
async function closeAll(cs) { for (const c of cs) { LOGS.push(...c.logs); await c.ctx.close(); } }

// deterministic RNG
function rng(seed) { let x = seed >>> 0; return () => { x = (x * 1664525 + 1013904223) >>> 0; return x / 2 ** 32; }; }

// ================================================================ S1: 3-user stress
async function s1(delay, own) {
  wsDelay = delay;
  const docId = `s1-3user-${own ? 'disjoint' : 'shared'}-${delay}`;
  const A = await openDoc(docId, 'alice'), B = await openDoc(docId, 'bob'), C = await openDoc(docId, 'charlie');
  const cs = [A, B, C];
  await Promise.all([
    ev(A, (o) => { for (let i = 0; i < 4; i++) window.__edit('add', o); }, own),
    ev(B, (o) => { for (let i = 0; i < 3; i++) window.__edit('add', o); }, own),
    ev(C, (o) => { for (let i = 0; i < 3; i++) window.__edit('add', o); }, own),
  ]);
  await settleAll(cs);
  const KINDS = ['add', 'move', 'fill', 'label', 'stroke', 'edge', 'resize', 'delEdge', 'delVertex', 'add', 'move', 'fill'];
  const rnd = { alice: rng(11 + delay), bob: rng(22 + delay), charlie: rng(33 + delay) };
  const press = { kbdUndo: 0, tbUndo: 0, kbdRedo: 0, tbRedo: 0, fallback: 0, changed: 0, noop: 0 };
  const editsDone = { alice: 0, bob: 0, charlie: 0 };
  for (const c of cs) await focusEditor(c);
  const doPress = async (c, kind) => {
    const b = await c.page.evaluate(() => window.__adapter.liveCanonHash());
    if (kind === 'kbdUndo') await c.page.keyboard.press(c.undoKey);
    else if (kind === 'kbdRedo') await c.page.keyboard.press(c.redoKey);
    else if ((await toolbar(c, kind === 'tbUndo' ? 'Undo' : 'Redo')) !== 'toolbar') press.fallback++;
    press[kind]++;
    const a = await c.page.evaluate(() => window.__adapter.liveCanonHash());
    if (a !== b) press.changed++; else press.noop++;
  };
  const ROUNDS = 6;
  for (let r = 0; r < ROUNDS; r++) {
    // concurrent edits (3 per user, no settle between users)
    await Promise.all(cs.map(async (c) => {
      for (let i = 0; i < 3; i++) {
        const k = KINDS[Math.floor(rnd[c.user]() * KINDS.length)];
        await ev(c, ([x, o]) => window.__edit(x, o), [k, own]);
        editsDone[c.user]++;
        if (rnd[c.user]() < 0.4) await sleep(Math.floor(rnd[c.user]() * 60));
      }
    }));
    // interleaved undo bursts, all three users at once (sometimes before acks arrive)
    await Promise.all([
      (async () => { for (let i = 0; i < 3; i++) await doPress(A, 'kbdUndo'); if (r % 2) await doPress(A, 'tbRedo'); })(),
      (async () => { for (let i = 0; i < 2; i++) await doPress(B, 'tbUndo'); await doPress(B, 'kbdUndo'); })(),
      (async () => { await doPress(C, 'kbdUndo'); await doPress(C, 'kbdUndo'); await doPress(C, 'kbdRedo'); if (r % 3 === 2) await doPress(C, 'tbUndo'); })(),
    ]);
    if (r % 2 === 1) await settleAll(cs, 3000); // half the rounds keep stacking without settling
  }
  // final drain burst: everybody undoes 4 more times
  await Promise.all(cs.map(async (c) => { for (let i = 0; i < 4; i++) await doPress(c, i % 2 ? 'tbUndo' : 'kbdUndo'); }));
  const conv = await converge(cs, docId);
  // Concurrent deletion can legitimately reject an atomic batch. Require a
  // fresh independent edit from EVERY user to persist after that rejection.
  const markers = [];
  for (const c of cs) {
    markers.push(await ev(c, () => window.__add('RECOVERY', 900, 500)));
    await settleAll(cs);
  }
  const recovery = await converge(cs, docId);
  const recoveryMarkers = (await Promise.all(cs.map(c => ev(c, ids => window.__cells(ids).every(Boolean), markers)))).every(Boolean);
  const t = await totals(cs, docId);
  const fin = await snap(A);
  const res = { delay, own, rounds: ROUNDS, editsDone, press, cellsFinal: fin.cells, recoveryMarkers, recoveryConverged: recovery.ok, conv, ...t };
  R.s1.push(res);
  await closeAll(cs);
  return res;
}

// ================================================================ S2: delete container then undo
async function s2(delay) {
  wsDelay = delay;
  const docId = `s2-container-${delay}`;
  const A = await openDoc(docId, 'alice'), B = await openDoc(docId, 'bob'), C = await openDoc(docId, 'charlie');
  const cs = [A, B, C];
  const b1 = await ev(B, () => window.__add('B1', 600, 60));
  const b2 = await ev(B, () => window.__add('B2', 600, 180));
  await settleAll(cs);
  const K = await ev(A, () => window.__add('K', 40, 40, 320, 220, 'swimlane;container=1;whiteSpace=wrap;html=1;'));
  await waitSettled(A);
  const k1 = await ev(A, (p) => window.__add('k1', 20, 50, 90, 40, null, p), K);
  await waitSettled(A);
  const k2 = await ev(A, (p) => window.__add('k2', 180, 140, 90, 40, null, p), K);
  await waitSettled(A);
  const ek = await ev(A, ([a, b]) => window.__addEdge('ek', a, b), [k1, k2]);
  await settleAll(cs);
  const ids = [K, k1, k2, ek];
  const before = await ev(C, (x) => window.__cells(x), ids);
  // concurrent: Alice deletes edge while Bob edits B1/B2
  await Promise.all([
    ev(A, (x) => window.__del([x]), ek),
    ev(B, ([x, y]) => { window.__move(x, 40, 30); window.__fill(y, '#00aa55'); }, [b1, b2]),
  ]);
  await Promise.all([
    ev(A, (x) => window.__del([x]), K),
    ev(B, (x) => window.__label(x, 'BobL'), b1),
  ]);
  await settleAll(cs);
  const afterDel = await Promise.all(cs.map((c) => ev(c, (x) => window.__cells(x).filter(Boolean).length, ids)));
  await focusEditor(A);
  await A.page.keyboard.press(A.undoKey); // undo delete container
  // Bob keeps editing while Alice undoes
  await ev(B, (x) => window.__fill(x, '#3366ff'), b1);
  await settleAll(cs);
  const afterU1 = await Promise.all(cs.map((c) => ev(c, (x) => window.__cells(x), ids)));
  await A.page.keyboard.press(A.undoKey); // undo delete edge
  await settleAll(cs);
  const afterU2 = await Promise.all(cs.map((c) => ev(c, (x) => window.__cells(x), ids)));
  const bob = await Promise.all(cs.map((c) => ev(c, (x) => window.__cells(x), [b1, b2])));
  const conv = await converge(cs, docId);
  const t = await totals(cs, docId);
  const present = (arr) => arr.map((c) => (c ? c.id === K ? 'K' : c.label : '-')).join(',');
  const res = {
    delay,
    before: present(before),
    cellsAfterDelete: afterDel,
    afterUndo1: afterU1.map(present),
    childrenParentOK: afterU1.every((a) => a[1] && a[2] && a[1].parent === K && a[2].parent === K),
    afterUndo2: afterU2.map(present),
    allRestored: afterU2.every((a) => a.every(Boolean)),
    bobEditsIntact: bob.every((p) => p[0] && p[0].label === 'BobL' && p[0].fill === '#3366ff' && p[0].x === 640 && p[1] && p[1].fill === '#00aa55'),
    bobB1: bob.map((p) => p[0] && `${p[0].label}/${p[0].fill}/x${p[0].x}`).join(' | '),
    conv, ...t,
  };
  R.s2.push(res);
  await closeAll(cs);
  return res;
}

// ================================================================ S3: true conflict (Bob deletes X)
async function s3(delay, mode) {
  wsDelay = delay;
  const docId = `s3-conflict-${delay}-${mode}`;
  const A = await openDoc(docId, 'alice'), B = await openDoc(docId, 'bob'), C = await openDoc(docId, 'charlie');
  const cs = [A, B, C];
  const X = await ev(A, () => window.__add('X', 40, 40)); await waitSettled(A);
  const Y = await ev(A, () => window.__add('Y', 300, 40)); await waitSettled(A);
  await ev(A, (x) => window.__fill(x, '#ff0000'), X); await waitSettled(A);
  await ev(A, (x) => window.__label(x, 'X2'), X); await waitSettled(A);
  await ev(A, (x) => window.__fill(x, '#0000ff'), Y); await waitSettled(A);
  await ev(A, (x) => window.__label(x, 'Y2'), Y); await settleAll(cs);
  const h0 = await snap(A);
  await ev(B, (x) => window.__del([x]), X);
  await settleAll(cs);
  const afterBob = await snap(A);
  await focusEditor(A);
  const steps = [];
  const desc = (c) => (c ? `${c.label}${c.fill ? '/' + c.fill : ''}` : '-');
  const N = 8;
  for (let i = 1; i <= N; i++) {
    const b = await snap(A);
    await A.page.keyboard.press(A.undoKey);
    if (mode === 'paced') await settleAll(cs);
    const a = await snap(A);
    const [x, y] = await ev(A, (ids) => window.__cells(ids), [X, Y]);
    steps.push(`u${i} nextAdd ${b.nextAdd}->${a.nextAdd} changed=${a.lh !== b.lh} X=${desc(x)} Y=${desc(y)} nacks=${srvNacks(docId)} throws+${a.throws - b.throws} mism+${a.mismatch - b.mismatch}`);
  }
  await settleAll(cs);
  const midConv = await converge(cs, docId, 12000);
  const xyAll = await Promise.all(cs.map((c) => ev(c, (ids) => window.__cells(ids), [X, Y])));
  const redo = [];
  for (let i = 1; i <= N; i++) {
    const b = await snap(A);
    if (i % 2) await A.page.keyboard.press(A.redoKey); else await toolbar(A, 'Redo');
    if (mode === 'paced') await settleAll(cs);
    const a = await snap(A);
    const [x, y] = await ev(A, (ids) => window.__cells(ids), [X, Y]);
    redo.push(`r${i} nextAdd ${b.nextAdd}->${a.nextAdd} changed=${a.lh !== b.lh} X=${desc(x)} Y=${desc(y)}`);
  }
  const conv = await converge(cs, docId);
  const xyEnd = await Promise.all(cs.map((c) => ev(c, (ids) => window.__cells(ids), [X, Y])));
  const t = await totals(cs, docId);
  const res = {
    delay, mode, histLenBefore: h0.histLen, nextAddAfterBobDelete: afterBob.nextAdd,
    steps, afterUndo: xyAll.map((p) => `X=${desc(p[0])} Y=${desc(p[1])}`), midConv: midConv.ok,
    redo, afterRedo: xyEnd.map((p) => `X=${desc(p[0])} Y=${desc(p[1])}`), conv, ...t,
  };
  R.s3.push(res);
  await closeAll(cs);
  return res;
}

// ================================================================ S4: multi-page delete + undo
async function s4(delay) {
  wsDelay = delay;
  const docId = `s4-pages-${delay}`;
  const A = await openDoc(docId, 'alice'), B = await openDoc(docId, 'bob'), C = await openDoc(docId, 'charlie');
  const cs = [A, B, C];
  const b1 = await ev(B, () => window.__add('BP1', 40, 40)); await settleAll(cs);
  await ev(A, () => { const ui = window.__adapter.editorUi(); const pg = ui.createPage('Page-2', ui.createPageId()); ui.insertPage(pg); ui.selectPage(pg); });
  await waitSettled(A);
  await ev(A, () => { window.__add('P2a', 40, 40); window.__add('P2b', 240, 40); });
  await waitSettled(A);
  await ev(A, () => { const ui = window.__adapter.editorUi(); ui.selectPage(ui.pages[0]); });
  await settleAll(cs);
  const p0 = await Promise.all(cs.map((c) => ev(c, () => window.__pages()))); p0.nk = srvNacks(docId);
  // concurrent: Alice deletes page 2 while Bob edits page 1
  await Promise.all([
    ev(A, () => { const ui = window.__adapter.editorUi(); ui.removePage(ui.pages[1]); }),
    ev(B, (x) => { window.__label(x, 'BobEdit'); window.__fill(x, '#00aa55'); window.__add('BP2', 240, 200); }, b1),
  ]);
  await settleAll(cs);
  const p1 = await Promise.all(cs.map((c) => ev(c, () => window.__pages()))); p1.nk = srvNacks(docId);
  await focusEditor(A);
  await A.page.keyboard.press(A.undoKey);
  await ev(B, (x) => window.__move(x, 10, 10), b1); // Bob keeps editing during the undo
  await settleAll(cs);
  const p2 = await Promise.all(cs.map((c) => ev(c, () => window.__pages()))); p2.nk = srvNacks(docId);
  const conv1 = await converge(cs, docId, 12000);
  // redo (delete again) + undo (restore again)
  await A.page.keyboard.press(A.redoKey); await settleAll(cs);
  const p3 = await Promise.all(cs.map((c) => ev(c, () => window.__pages()))); p3.nk = srvNacks(docId);
  await toolbar(A, 'Undo'); await settleAll(cs);
  const p4 = await Promise.all(cs.map((c) => ev(c, () => window.__pages()))); p4.nk = srvNacks(docId);
  const bob = await Promise.all(cs.map((c) => ev(c, (x) => window.__cells([x])[0], b1)));
  const conv = await converge(cs, docId);
  const t = await totals(cs, docId);
  const fmt = (ps) => ps.map((p) => p.confirmed.join('+')).join(' | ') + ` [nacks=${ps.nk}]`;
  const res = {
    delay, initial: fmt(p0), afterDelete: fmt(p1), afterUndo: fmt(p2), afterRedo: fmt(p3), afterUndo2: fmt(p4),
    page2RestoredAll: p2.every((p) => p.confirmed.some((x) => x === 'Page-2:2')) && p4.every((p) => p.confirmed.some((x) => x === 'Page-2:2')),
    uiPagesAll: p4.map((p) => p.ui.length).join(','),
    bobEditsIntact: bob.every((c) => c && c.label === 'BobEdit' && c.fill === '#00aa55'),
    conv1: conv1.ok, conv, ...t,
  };
  R.s4.push(res);
  await closeAll(cs);
  return res;
}

// ================================================================ S5: WS drop / reconnect
async function waitJoined(s, ms = 20000) {
  const end = Date.now() + ms;
  while (Date.now() < end) { if (await s.page.evaluate(() => window.__adapter.state().joined)) return true; await sleep(50); }
  return false;
}
async function s5(delay, kind) {
  wsDelay = delay;
  const docId = `s5-${kind}-${delay}`;
  const A = await openDoc(docId, 'alice'), B = await openDoc(docId, 'bob');
  const cs = [A, B];
  await ev(A, () => { window.__add('A1', 40, 40); window.__add('A2', 240, 40); window.__add('A3', 440, 40); });
  await ev(B, () => window.__add('B1', 40, 240));
  await settleAll(cs);
  const pre = await snap(A);
  let gapInfo = {};
  if (kind === 'resync') {
    await ev(A, () => window.__adapter.resync('test_drop'));
    await ev(B, () => window.__edit('label')); // remote edit lands while Alice reconnects
    gapInfo.rejoined = await waitJoined(A);
  } else {
    // real offline gap: close both legs and refuse reconnects for 2.5s, edit on both sides meanwhile
    A.net.blocked = true;
    try { A.net.client.close({ code: 4001, reason: 'test drop' }); } catch (e) { gapInfo.closeErr = String(e).slice(0, 80); }
    try { A.net.server.close({ code: 1000 }); } catch { /* already closed */ }
    await sleep(200);
    gapInfo.offlineSeen = !(await ev(A, () => window.__adapter.state().joined));
    const offA = await ev(A, () => window.__add('OFFLINE-A', 640, 240));
    await ev(A, () => window.__edit('fill'));
    await ev(B, () => window.__edit('label'));
    await sleep(2300);
    A.net.blocked = false;
    gapInfo.rejoined = await waitJoined(A);
    gapInfo.refused = A.net.refused;
    await settleAll(cs);
    const off = await Promise.all(cs.map((c) => ev(c, (x) => !!window.__cells([x])[0], offA)));
    gapInfo.offlineEditSurvived = off.join(',');
  }
  await settleAll(cs);
  const post = await snap(A);
  gapInfo.histLen = `${pre.histLen}->${post.histLen}`;
  gapInfo.nextAdd = `${pre.nextAdd}->${post.nextAdd}`;
  gapInfo.convAfterReconnect = (await converge(cs, docId, 12000)).ok;
  // further edits + undo/redo
  await ev(A, () => { window.__edit('fill'); window.__edit('label'); window.__edit('add'); });
  await ev(B, () => { window.__edit('move'); window.__edit('stroke'); });
  await focusEditor(A);
  const st = [];
  for (const k of ['u', 'u', 'u', 'r', 'u']) {
    const b = await snap(A);
    if (k === 'u') await A.page.keyboard.press(A.undoKey); else await toolbar(A, 'Redo');
    await settleAll(cs);
    const a = await snap(A);
    st.push(`${k}:${b.nextAdd}->${a.nextAdd}${a.lh !== b.lh ? '' : '(noop)'}`);
  }
  // undo back across the reconnect boundary
  for (let i = 0; i < 4; i++) { await A.page.keyboard.press(A.undoKey); }
  const conv = await converge(cs, docId);
  const t = await totals(cs, docId);
  const res = { delay, kind, gapInfo, undoRedo: st.join(' '), conv, ...t };
  R.s5.push(res);
  await closeAll(cs);
  return res;
}

// ================================================================ runner
async function safe(name, fn) {
  try { return await fn(); }
  catch (e) { R.errors.push(`${name}: ${String(e).slice(0, 300)}`); console.log(`ERROR in ${name}:`, String(e).slice(0, 300)); return null; }
}
const line = (tag, r) => {
  if (!r) return;
  const pass = checkCollab(tag, r);
  r.pass = pass;
  const s = `${pass ? (r.serverNacks ? 'PASS_WITH_LIFECYCLE_CONFLICT' : 'PASS') : 'FAIL'} ${tag} conv=${r.conv && r.conv.ok}(${r.conv && r.conv.ms}ms, srv=${r.conv && r.conv.server}) nacks=${r.serverNacks} throws=${r.throws} mismatch=${r.mismatch} resyncs=${r.resyncs} hashBad=${r.hashBad} pageErr=${r.pageErrors}`;
  R.summary.push(s);
  console.log(s);
  if (!pass && r.conv) console.log('   ', r.conv.clients.join(' '), r.nackCodes, r.undoErrs);
  if (!pass) for (const n of (r.nackDetail || [])) console.log('    nack:', n);
  if (r.conv && r.conv.stuck) for (const x of r.conv.stuck) console.log('    STUCK:', JSON.stringify(x));
};

const T0 = Date.now();
const ONLY = process.env.ONLY ? process.env.ONLY.split(',') : null;
const want = (k) => !ONLY || ONLY.includes(k);
const REPEAT = +(process.env.REPEAT || 1);
try {
  for (let rep = 0; rep < REPEAT; rep++) for (const d of DELAYS) {
    console.log(`\n######## wsDelay=${d}ms ########`);
    if (want('s1')) for (const own of [false, true]) {
      const tag = own ? 'disjoint' : 'shared';
      console.log(`== S1 3-user concurrent edits + undo/redo bursts (${tag} cells) ==`);
      const r1 = await safe(`s1 ${d} ${tag}`, () => s1(d, own));
      if (r1) console.log(`   edits=${JSON.stringify(r1.editsDone)} presses=${JSON.stringify(r1.press)} cellsFinal=${r1.cellsFinal}`);
      line(`S1 3user-${tag} d=${d}`, r1);
    }

    if (want('s2')) {
    console.log('== S2 container delete -> undo x2 under collab ==');
    const r2 = await safe(`s2 ${d}`, () => s2(d));
    if (r2) console.log(`   before=${r2.before} afterDelete(cells A/B/C)=${r2.cellsAfterDelete} undo1=${r2.afterUndo1.join(' | ')} parentOK=${r2.childrenParentOK} undo2=${r2.afterUndo2.join(' | ')} allRestored=${r2.allRestored} bobIntact=${r2.bobEditsIntact} (${r2.bobB1})`);
    line(`S2 container d=${d}`, r2);
    }

    if (want('s3')) for (const mode of ['paced', 'rapid']) {
      console.log(`== S3 conflict (Bob deletes X) ${mode} ==`);
      const r3 = await safe(`s3 ${d} ${mode}`, () => s3(d, mode));
      if (r3) {
        console.log(`   histLen=${r3.histLenBefore} nextAddAfterBobDel=${r3.nextAddAfterBobDelete}`);
        for (const s of r3.steps) console.log('   ' + s);
        console.log(`   afterUndo(A/B/C): ${r3.afterUndo.join(' | ')} midConv=${r3.midConv}`);
        for (const s of r3.redo) console.log('   ' + s);
        console.log(`   afterRedo(A/B/C): ${r3.afterRedo.join(' | ')}`);
      }
      line(`S3 conflict ${mode} d=${d}`, r3);
    }

    if (want('s4')) {
    console.log('== S4 multi-page delete + undo ==');
    const r4 = await safe(`s4 ${d}`, () => s4(d));
    if (r4) console.log(`   init=${r4.initial}\n   del=${r4.afterDelete}\n   undo=${r4.afterUndo}\n   redo=${r4.afterRedo}\n   undo2=${r4.afterUndo2}\n   page2RestoredAll=${r4.page2RestoredAll} uiPages=${r4.uiPagesAll} bobIntact=${r4.bobEditsIntact} conv1=${r4.conv1}`);
    line(`S4 pages d=${d}`, r4);
    }

    if (want('s5')) for (const kind of ['resync', 'offline']) {
      console.log(`== S5 ws ${kind} ==`);
      const r5 = await safe(`s5 ${d} ${kind}`, () => s5(d, kind));
      if (r5) console.log(`   gap=${JSON.stringify(r5.gapInfo)} undoRedo=${r5.undoRedo}`);
      line(`S5 ${kind} d=${d}`, r5);
    }
  }
} finally {
  R.logs = [...new Set(LOGS)];
  R.elapsedSec = Math.round((Date.now() - T0) / 1000);
  fs.writeFileSync(OUT, JSON.stringify(R, null, 2));
  console.log(`\n==== SUMMARY (${R.elapsedSec}s) ====`);
  for (const s of R.summary) console.log(s);
  console.log(`errors=${R.errors.length}${R.errors.length ? ' ' + JSON.stringify(R.errors) : ''}`);
  console.log(`console warn/err lines=${R.logs.length}`);
  for (const l of R.logs.slice(0, 15)) console.log('  ' + l);
  console.log('full details:', OUT);
  if (R.errors.length || !R.summary.length || R.summary.some(s => s.includes('FAIL'))) process.exitCode = 1;
  await browser.close();
  server.close();
}
