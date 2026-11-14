// massive-solo.mjs — bounded solo Undo/Redo suite (real Chrome via playwright-core) against repro-server.mjs
// (live drawio-custom-service internal/edge/web/* + govalidator-bin). Read-only w.r.t. the service repo.
// Scenarios x wsDelay {0,150,400} x mode {kbd, toolbar}:
//   S1 deep nested container/swimlane + table + vertices + edge, cascade delete, U/R/U, undo to blank, redo up
//   S2 group/ungroup + duplicate/clone + reconnect source/target + delete, U/R/U, undo to blank, redo up
//   S3 rapid-fire burst (24 edits, no settle) + rapid undo every 30-50ms (+3 past 0) + rapid redo back up
//   S4a inline label editing (undo during edit vs after commit), S4b Format panel inputs/buttons,
//   S4c multi-page (add p2/p3, rename, switch, draw/delete on p2, delete p2 -> undo -> switch back -> undo p2 edits),
//   S4d real Sidebar shapes (Misc, UML, Entity Relation, Flowchart)
import { createRequire } from 'node:module';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const require = createRequire(new URL('../package.json', import.meta.url));
const { chromium } = require('playwright-core');
import { startServer } from './repro-server.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const OUT = path.join(HERE, 'massive-solo.json');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const DELAYS = (process.env.DELAYS || '0,150,400').split(',').map(Number);
const MODES = (process.env.MODES || 'kbd,toolbar').split(',');
const ONLY = process.env.ONLY ? process.env.ONLY.split(',') : null;
let wsDelay = 0;
const RUNS = [];
const ERRORS = [];
const LOGS = [];
const { server, port, docs } = await startServer(0, { wsDelayMs: () => wsDelay });
const browser = await chromium.launch({
  executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
});

// ---------------------------------------------------------------- page-side hooks
function installHooks() {
  const ui = window.__adapter.editorUi(), um = ui.editor.undoManager;
  const D = (window.__deep = { undo: 0, redo: 0, throws: 0, mismatch: 0, errs: [] });
  const wrap = (name) => {
    const o = um[name];
    um[name] = function () {
      D[name]++;
      try { return o.apply(this, arguments); }
      catch (e) { D.throws++; D.errs.push(name + ': ' + String(e).slice(0, 100)); throw e; }
    };
  };
  wrap('undo'); wrap('redo');
  const B = window.DrawioBridge, oDiff = B.diff;
  B.diff = function () { const r = oDiff.apply(this, arguments); if (r.mismatch) D.mismatch++; return r; };
  const W = document.getElementById('editor').contentWindow;
  window.__G = () => { const g = ui.editor.graph; return { ui, g, p: g.getDefaultParent(), W, m: g.getModel() }; };
  window.__R = {};
  window.__cell = (id) => ui.editor.graph.getModel().getCell(window.__R[id] || id);
}

async function openDoc(docId) {
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  const logs = [];
  page.on('console', (m) => {
    if (m.type() === 'warning' || m.type() === 'error') {
      const t = m.text();
      if (!/404|Failed to load resource/.test(t)) logs.push(`[${docId}][${m.type()}] ${t.slice(0, 180)}`);
    }
  });
  page.on('pageerror', (e) => logs.push(`[${docId}][pageerror] ${String(e).slice(0, 180)}`));
  await page.goto(`http://127.0.0.1:${port}/d/${docId}?user=alice`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && window.__adapter.editorUi(), null, { timeout: 30000 });
  const frame = page.frameLocator('#editor');
  await frame.locator('.geSidebarContainer .geItem').first().waitFor({ timeout: 20000 });
  await page.evaluate(installHooks);
  const isMac = await page.evaluate(() => document.getElementById('editor').contentWindow.mxClient.IS_MAC);
  return { ctx, page, frame, logs, docId, undoKey: isMac ? 'Meta+z' : 'Control+z', redoKey: isMac ? 'Meta+Shift+z' : 'Control+y', selAll: isMac ? 'Meta+a' : 'Control+a' };
}

async function snap(s) {
  const r = await s.page.evaluate(async () => {
    const a = window.__adapter, ui = a.editorUi(), um = ui.editor.undoManager, g = ui.editor.graph;
    const lh = await a.liveCanonHash();
    const ch = await a.modelCanonHash('confirmed');
    const D = window.__deep;
    return {
      lh, ch, nextAdd: um.indexOfNextAdd, histLen: um.history.length, pages: ui.pages.length,
      pageId: ui.currentPage ? ui.currentPage.getId() : '',
      cells: Object.values(g.getModel().cells).filter((c) => c.vertex || c.edge).length,
      patches: a.stats.patches, nacked: a.stats.nacked, resyncs: a.stats.resyncs, hashBad: a.stats.hashBad || 0,
      pending: a.state().pending, busy: a.state().busy, editing: g.isEditing(),
      mismatch: D.mismatch, throws: D.throws, undoCalls: D.undo, redoCalls: D.redo,
    };
  });
  r.nacks = docs.get(s.docId)?.nacks.length ?? 0;
  return r;
}

async function waitSettled(page, ms = 8000) {
  await sleep(60);
  const end = Date.now() + ms;
  while (Date.now() < end) {
    const st = await page.evaluate(() => window.__adapter.state());
    if (st.pending === 0 && !st.busy) return true;
    await sleep(40);
  }
  return false;
}

async function focusEditor(s) { await s.page.mouse.click(700, 760); await sleep(60); }

const nextAddOf = (s) => s.page.evaluate(() => window.__adapter.editorUi().editor.undoManager.indexOfNextAdd);
const TB = { clickErrors: [], lateClicks: 0 };
async function toolbar(s, which) {
  const before = await nextAddOf(s);
  try {
    await s.frame.locator(`[title^="${which}"]:visible`).first().click({ timeout: 1500 });
    return true;
  } catch (e) {
    TB.clickErrors.push(`${s.docId} ${which}: ${String(e).split('\n').filter((l) => /intercept|not visible|not stable|disabled|Timeout/.test(l)).slice(0, 2).join(' / ').slice(0, 200)}`);
    // the click may have landed even though Playwright reported a timeout: never apply the action twice
    if ((await nextAddOf(s)) !== before) { TB.lateClicks++; return true; }
    await s.page.evaluate((w) => window.__adapter.editorUi().actions.get(w.toLowerCase()).funct(), which);
    return false;
  }
}

async function doOp(s, op, mode, run) {
  if (mode === 'kbd') await s.page.keyboard.press(op === 'U' ? s.undoKey : s.redoKey);
  else if (!(await toolbar(s, op === 'U' ? 'Undo' : 'Redo'))) run.fallback++;
}

// ---------------------------------------------------------------- chain + walker
// step(): run an in-page edit, optionally settle, snapshot, push to the chain when the hash changed.
async function step(s, chain, label, fn, arg, { settle = true } = {}) {
  await s.page.evaluate(fn, arg);
  if (settle) await waitSettled(s.page);
  const a = await snap(s);
  const prev = chain[chain.length - 1];
  if (a.lh !== prev.lh) { a.label = label; chain.push(a); return true; }
  return false; // e.g. pure page switch
}

function newRun(scn, delay, mode, docId) {
  return { scn, delay, mode, docId, counts: { ok: 0, END: 0, PAGESEL: 0, NATIVE_NOOP: 0, NOOP: 0, MULTI: 0, LAG: 0, UNKNOWN: 0 }, fallback: 0, steps: [], checks: {}, probes: {} };
}

// One undo/redo with classification against the chain of known states.
async function walkOne(s, chain, run, op, mode, top, cur, gap) {
  const b = await snap(s);
  await doOp(s, op, mode, run);
  if (gap) await sleep(gap);
  const a = await snap(s);
  const expected = op === 'U' ? (cur > 0 ? cur - 1 : cur) : (cur < top ? cur + 1 : cur);
  let st, next = cur;
  if (a.lh === b.lh) {
    if (a.pageId !== b.pageId) st = 'PAGESEL';
    else if (expected === cur) st = 'END';
    else if (run.allowNoop > 0) { run.allowNoop--; st = 'NATIVE_NOOP'; }
    else st = 'NOOP';
  } else {
    const cands = chain.map((c, i) => (c.lh === a.lh ? i : -1)).filter((i) => i >= 0);
    if (!cands.length) st = 'UNKNOWN';
    else {
      const j = cands.reduce((best, c) => (Math.abs(c - expected) < Math.abs(best - expected) ? c : best));
      if (j === expected) st = 'ok';
      else if ((op === 'U' && j < expected) || (op === 'R' && j > expected)) st = `MULTI(${j - expected})`;
      else st = `LAG(${j - expected})`;
      next = j;
    }
  }
  run.counts[st.split('(')[0]]++;
  run.steps.push(`${op} ${st} ${cur}->${next} nextAdd ${b.nextAdd}->${a.nextAdd} pend=${a.pending} pg=${a.pageId.slice(0, 6)}`);
  return next;
}

// Undo until state 0 (PAGESEL steps allowed), then `extra` more; returns cur.
async function undoToZero(s, chain, run, mode, top, cur, { extra = 2, gap = 0 } = {}) {
  let guard = chain.length * 3 + 10;
  while (cur > 0 && guard-- > 0) cur = await walkOne(s, chain, run, 'U', mode, top, cur, typeof gap === 'function' ? gap() : gap);
  for (let i = 0; i < extra; i++) cur = await walkOne(s, chain, run, 'U', mode, top, cur, typeof gap === 'function' ? gap() : gap);
  return cur;
}
async function redoToTop(s, chain, run, mode, top, cur, { extra = 1, gap = 0 } = {}) {
  let guard = chain.length * 3 + 10;
  while (cur < top && guard-- > 0) {
    const before = cur;
    cur = await walkOne(s, chain, run, 'R', mode, top, cur, typeof gap === 'function' ? gap() : gap);
    if (cur === before && run.steps[run.steps.length - 1].includes('END')) break;
  }
  for (let i = 0; i < extra; i++) cur = await walkOne(s, chain, run, 'R', mode, top, cur, typeof gap === 'function' ? gap() : gap);
  return cur;
}

async function checkpoint(s, chain, run, name, idx) {
  await sleep(120);
  await waitSettled(s.page);
  const f = await snap(s);
  run.checks[name] = { atExpected: f.lh === chain[idx].lh, converged: f.lh === f.ch, idx };
  return f;
}

async function finish(s, run) {
  await sleep(120);
  await waitSettled(s.page);
  const f = await snap(s);
  const srv = docs.get(s.docId);
  Object.assign(run, {
    nacks: srv.nacks.length, nackCodes: srv.nacks.slice(0, 5).map((n) => `${n.code}@${n.index}`),
    resyncs: f.resyncs, hashBad: f.hashBad, throws: f.throws, mismatch: f.mismatch, patches: f.patches,
    finalConverged: f.lh === f.ch,
    throwErrs: await s.page.evaluate(() => window.__deep.errs.slice(0, 3)),
  });
  const c = run.counts;
  run.pass = c.MULTI === 0 && c.LAG === 0 && c.UNKNOWN === 0 && c.NOOP === 0 && run.nacks === 0 && run.resyncs === 0 &&
    run.hashBad === 0 && run.throws === 0 && run.mismatch === 0 && run.finalConverged &&
    Object.values(run.checks).every((x) => x.atExpected && x.converged) &&
    Object.values(run.probes).every((p) => p.pass !== false);
  RUNS.push(run);
  LOGS.push(...s.logs);
  await s.ctx.close();
  const pr = Object.entries(run.probes).map(([k, v]) => `${k}:${v.pass === false ? 'FAIL' : 'ok'}`).join(',');
  console.log(`${run.pass ? 'PASS' : 'FAIL'} ${run.scn} d=${run.delay} ${run.mode} chain=${run.chainLen} ${JSON.stringify(c)} fb=${run.fallback} nacks=${run.nacks} resyncs=${run.resyncs} hashBad=${run.hashBad} throws=${run.throws} mismatch=${run.mismatch} conv=${run.finalConverged} checks=${Object.entries(run.checks).map(([k, v]) => `${k}:${v.atExpected && v.converged ? 'ok' : 'BAD'}`).join(',')}${pr ? ' probes=' + pr : ''}`);
  if (!run.pass) for (const l of run.steps.filter((x) => !/ ok | END | PAGESEL | NATIVE_NOOP /.test(x)).slice(0, 6)) console.log('   ', l);
  if (run.nackCodes.length) console.log('    nacks:', run.nackCodes.join(' '));
  if (run.throwErrs.length) console.log('    throws:', run.throwErrs.join(' | '));
}

// Standard tail for S1/S2: U (undo delete) R (redo delete) U (undo delete), undo to blank (+2), redo to top (+1).
async function standardWalk(s, chain, run, mode) {
  const top = chain.length - 1;
  let cur = top;
  cur = await walkOne(s, chain, run, 'U', mode, top, cur, 0);
  cur = await walkOne(s, chain, run, 'R', mode, top, cur, 0);
  cur = await walkOne(s, chain, run, 'U', mode, top, cur, 0);
  await checkpoint(s, chain, run, 'afterUndoDelete', top - 1);
  cur = await undoToZero(s, chain, run, mode, top, cur, { extra: 2 });
  await checkpoint(s, chain, run, 'blank', 0);
  cur = await redoToTop(s, chain, run, mode, top, cur, { extra: 1 });
  await checkpoint(s, chain, run, 'redoTop', top);
}

// ---------------------------------------------------------------- S1 deep nested
async function S1(delay, mode) {
  wsDelay = delay;
  const docId = `s1-${delay}-${mode}`;
  const s = await openDoc(docId);
  await waitSettled(s.page);
  const run = newRun('S1-nested', delay, mode, docId);
  const chain = [await snap(s)];
  const E = [
    ['outer', () => { const { g, p } = __G(); __R.outer = g.insertVertex(p, null, 'Outer', 40, 40, 560, 380, 'swimlane;startSize=30;container=1;').id; }],
    ['inner', () => { const { g } = __G(); __R.inner = g.insertVertex(__cell('outer'), null, 'Inner', 20, 40, 220, 180, 'swimlane;startSize=24;').id; }],
    ['table', () => { const { g } = __G(); const t = g.createTable(2, 2, 60, 30, 'Tbl', 30); t.geometry.x = 280; t.geometry.y = 40; g.addCells([t], __cell('outer')); __R.table = t.id; }],
    ['v1', () => { const { g } = __G(); __R.v1 = g.insertVertex(__cell('inner'), null, 'V1', 30, 50, 100, 50, 'rounded=1;whiteSpace=wrap;html=1;').id; }],
    ['v2', () => { const { g } = __G(); __R.v2 = g.insertVertex(__cell('outer'), null, 'V2', 300, 260, 100, 50, 'ellipse;whiteSpace=wrap;html=1;').id; }],
    ['edge', () => { const { g } = __G(); __R.e = g.insertEdge(__cell('outer'), null, 'link', __cell('v1'), __cell('v2'), 'edgeStyle=orthogonalEdgeStyle;').id; }],
    ['moveOuter', () => { const { g } = __G(); g.moveCells([__cell('outer')], 60, 40); }],
    ['resizeV1', () => { const { g } = __G(); const v = __cell('v1'); const geo = v.geometry.clone(); geo.width += 40; geo.height += 20; g.resizeCell(v, geo); }],
    ['styleInner', () => { const { g } = __G(); g.setCellStyles('fillColor', '#dae8fc', [__cell('inner')]); }],
    ['styleV2font', () => { const { g } = __G(); g.setCellStyles('fontStyle', 1, [__cell('v2')]); }],
    ['labelTable', () => { const { g } = __G(); g.cellLabelChanged(__cell('table'), 'Tbl-2'); }],
    ['deleteOuterCascade', () => { const { g } = __G(); g.removeCells([__cell('outer')], true); }],
  ];
  for (const [l, fn] of E) await step(s, chain, l, fn);
  run.chainLen = chain.length - 1;
  run.cellsTop = chain[chain.length - 2].cells; run.cellsAfterDelete = chain[chain.length - 1].cells;
  await focusEditor(s);
  await standardWalk(s, chain, run, mode);
  await finish(s, run);
}

// ---------------------------------------------------------------- S2 group/clone/reconnect
async function S2(delay, mode) {
  wsDelay = delay;
  const docId = `s2-${delay}-${mode}`;
  const s = await openDoc(docId);
  await waitSettled(s.page);
  const run = newRun('S2-group', delay, mode, docId);
  const chain = [await snap(s)];
  const E = [
    ['a', () => { const { g, p } = __G(); __R.a = g.insertVertex(p, null, 'A', 60, 60, 100, 50).id; }],
    ['b', () => { const { g, p } = __G(); __R.b = g.insertVertex(p, null, 'B', 260, 60, 100, 50).id; }],
    ['c', () => { const { g, p } = __G(); __R.c = g.insertVertex(p, null, 'C', 60, 260, 100, 50, 'rhombus;').id; }],
    ['edgeAB', () => { const { g, p } = __G(); __R.e = g.insertEdge(p, null, 'ab', __cell('a'), __cell('b')).id; }],
    ['group', () => { const { g } = __G(); __R.grp = g.groupCells(null, 10, [__cell('a'), __cell('b'), __cell('e')]).id; }],
    ['duplicateC', () => { const { g, ui } = __G(); g.setSelectionCell(__cell('c')); ui.actions.get('duplicate').funct(); __R.dup = g.getSelectionCell().id; g.clearSelection(); }],
    ['cloneDragC', () => { const { g } = __G(); __R.cl = g.moveCells([__cell('c')], 300, 0, true)[0].id; }],
    ['reconnectSrc', () => { const { g } = __G(); g.connectCell(__cell('e'), __cell('dup'), true); }],
    ['reconnectTgt', () => { const { g } = __G(); g.connectCell(__cell('e'), __cell('cl'), false); }],
    ['ungroup', () => { const { g } = __G(); g.ungroupCells([__cell('grp')]); }],
    ['moveB', () => { const { g } = __G(); g.moveCells([__cell('b')], 20, 30); }],
    ['deleteClone+edge', () => { const { g } = __G(); g.removeCells([__cell('cl')], true); }],
  ];
  for (const [l, fn] of E) await step(s, chain, l, fn);
  run.chainLen = chain.length - 1;
  await focusEditor(s);
  await standardWalk(s, chain, run, mode);
  await finish(s, run);
}

// ---------------------------------------------------------------- S3 rapid-fire burst
function installBurst() {
  window.__k = 0;
  window.__edit = function (kind) {
    const { g, p } = __G();
    const vs = g.getChildVertices(p), es = g.getChildEdges(p);
    const pick = (arr, i) => arr[((i % arr.length) + arr.length) % arr.length];
    const k = ++window.__k;
    const x = 40 + (k % 5) * 150, y = 40 + Math.floor(k / 5) * 110;
    switch (kind) {
      case 'add': return g.insertVertex(p, null, 'N' + k, x, y, 120, 60, 'rounded=1;whiteSpace=wrap;html=1;');
      case 'table': { const t = g.createTable(2, 3, 50, 30, 'T' + k, 30); t.geometry.x = x; t.geometry.y = y; return g.addCells([t], p); }
      case 'swimlane': return g.insertVertex(p, null, 'S' + k, x, y, 240, 160, 'swimlane;');
      case 'edge': return g.insertEdge(p, null, 'e' + k, pick(vs, 0), pick(vs, 1));
      case 'move': return g.moveCells([pick(vs, 0)], 30, 20);
      case 'resize': { const v = pick(vs, 1); const geo = v.geometry.clone(); geo.width += 40; geo.height += 20; return g.resizeCell(v, geo); }
      case 'label': return g.cellLabelChanged(pick(vs, 0), 'L' + k);
      case 'fill': return g.setCellStyles('fillColor', '#' + ['ff8800', '00aa55', '3366ff'][k % 3], [pick(vs, 2)]);
      case 'stroke': return g.setCellStyles('strokeColor', '#aa' + String(k).padStart(4, '0'), [pick(vs, 0)]);
      case 'font': return g.setCellStyles('fontSize', 10 + k, [pick(vs, 1)]);
      case 'delEdge': return g.removeCells([pick(es, -1)]);
      case 'delVertex': return g.removeCells([pick(vs, -1)], true);
    }
    throw new Error('unknown edit ' + kind);
  };
}
const BURST = ['add', 'add', 'add', 'add', 'edge', 'edge', 'move', 'resize', 'label', 'fill', 'stroke', 'font', 'table', 'swimlane',
  'delEdge', 'delVertex', 'add', 'add', 'edge', 'label', 'delVertex', 'move', 'fill', 'delVertex', 'font', 'delEdge'];

async function S3(delay, mode) {
  wsDelay = delay;
  const docId = `s3-${delay}-${mode}`;
  const s = await openDoc(docId);
  await waitSettled(s.page);
  await s.page.evaluate(installBurst);
  const run = newRun('S3-burst', delay, mode, docId);
  const chain = [await snap(s)];
  let maxPending = 0;
  for (const e of BURST) {
    await step(s, chain, e, (k) => window.__edit(k), e, { settle: false });
    maxPending = Math.max(maxPending, chain[chain.length - 1].pending);
    await sleep(15);
  }
  run.chainLen = chain.length - 1;
  run.burstEdits = BURST.length;
  run.burstDeletes = BURST.filter((x) => x.startsWith('del')).length;
  await focusEditor(s);
  const top = chain.length - 1;
  const gap = () => 30 + Math.floor(Math.random() * 21);
  const pendAtFirstUndo = (await snap(s)).pending;
  let cur = await undoToZero(s, chain, run, mode, top, top, { extra: 3, gap });
  run.maxPendingDuringBurst = Math.max(maxPending, pendAtFirstUndo);
  run.pendingAtFirstUndo = pendAtFirstUndo;
  run.maxPendingDuringUndo = Math.max(...run.steps.map((x) => +/pend=(\d+)/.exec(x)[1]));
  await checkpoint(s, chain, run, 'blank', 0);
  cur = await redoToTop(s, chain, run, mode, top, cur, { extra: 1, gap });
  await checkpoint(s, chain, run, 'redoTop', top);
  // and rapid undo back to blank once more: final == initial
  cur = await undoToZero(s, chain, run, mode, top, cur, { extra: 1, gap });
  await checkpoint(s, chain, run, 'finalBlank', 0);
  await finish(s, run);
}

// ---------------------------------------------------------------- S4a inline label editing
async function cellCenter(s, idKey) {
  return s.page.evaluate((k) => {
    const f = document.getElementById('editor').getBoundingClientRect();
    const { g } = __G();
    const st = g.view.getState(__cell(k)), cont = g.container.getBoundingClientRect();
    return { x: f.left + cont.left + st.x - g.container.scrollLeft + st.width / 2, y: f.top + cont.top + st.y - g.container.scrollTop + st.height / 2 };
  }, idKey);
}

async function S4inline(delay, mode) {
  wsDelay = delay;
  const docId = `s4a-${delay}-${mode}`;
  const s = await openDoc(docId);
  await waitSettled(s.page);
  const run = newRun('S4a-inline', delay, mode, docId);
  const chain = [await snap(s)];
  await step(s, chain, 'add', () => { const { g, p } = __G(); __R.v = g.insertVertex(p, null, 'Orig', 300, 200, 160, 70, 'rounded=1;whiteSpace=wrap;html=1;').id; });
  const editType = async (text, replace) => {
    const pos = await cellCenter(s, 'v');
    await s.page.mouse.dblclick(pos.x, pos.y); await sleep(200);
    if (replace) { await s.page.keyboard.press(s.selAll); await sleep(50); }
    await s.page.keyboard.type(text, { delay: 15 }); await sleep(120);
  };
  // commit A: append text, commit by clicking canvas
  await editType('Hello', false);
  await focusEditor(s); await waitSettled(s.page);
  { const a = await snap(s); if (a.lh !== chain.at(-1).lh) { a.label = 'commitA'; chain.push(a); } }
  // commit B: replace text
  await editType('World', true);
  await focusEditor(s); await waitSettled(s.page);
  { const a = await snap(s); if (a.lh !== chain.at(-1).lh) { a.label = 'commitB'; chain.push(a); } }
  run.commitsRecorded = chain.length - 2;
  // probe 1: undo WHILE editing (kbd always; for toolbar mode also exercise toolbar while editing below)
  {
    const b = await snap(s);
    await editType('XYZ', false);
    const mid = await snap(s);
    await s.page.keyboard.press(s.undoKey); await sleep(150);
    const a = await snap(s);
    await s.page.evaluate(() => __G().g.stopEditing(true)); // cancel the editor
    await waitSettled(s.page);
    const c = await snap(s);
    run.probes.kbdUndoWhileEditing = {
      editingAtUndo: mid.editing, stillEditingAfter: a.editing, graphUndoCalls: a.undoCalls - b.undoCalls,
      modelUntouched: a.lh === b.lh, afterCancelAtTop: c.lh === chain.at(-1).lh,
      pass: mid.editing && a.lh === b.lh && c.lh === chain.at(-1).lh,
    };
  }
  if (mode === 'toolbar') {
    const b = await snap(s);
    await editType('Q', false);
    await toolbar(s, 'Undo'); await sleep(200); await waitSettled(s.page);
    const a = await snap(s);
    // draw.io's undo action while editing = native text undo inside the cell editor (no graph undo), like Ctrl+Z
    await s.page.evaluate(() => __G().g.stopEditing(true));
    await waitSettled(s.page);
    const c = await snap(s);
    run.probes.toolbarUndoWhileEditing = { stillEditingAfterClick: a.editing, graphUndoCalls: a.undoCalls - b.undoCalls, modelUntouched: a.lh === b.lh, afterCancelAtTop: c.lh === chain.at(-1).lh, pass: a.lh === b.lh && c.lh === chain.at(-1).lh };
  }
  run.chainLen = chain.length - 1;
  await focusEditor(s);
  const top = chain.length - 1;
  let cur = top;
  cur = await undoToZero(s, chain, run, mode, top, cur, { extra: 2 });
  await checkpoint(s, chain, run, 'blank', 0);
  cur = await redoToTop(s, chain, run, mode, top, cur, { extra: 0 });
  await checkpoint(s, chain, run, 'redoTop', top);
  await finish(s, run);
}

// ---------------------------------------------------------------- S4b Format panel
async function fmtTab(s, name) {
  const ok = await s.page.evaluate((n) => {
    const doc = document.getElementById('editor').contentDocument;
    const c = doc.querySelector('.geFormatContainer');
    if (!c) return false;
    const t = [...c.querySelectorAll('div')].find((d) => d.children.length === 0 && d.textContent.trim() === n && d.offsetParent !== null);
    if (!t) return false;
    t.setAttribute('data-ms-tab', n); return true;
  }, name);
  if (ok) await s.frame.locator(`[data-ms-tab="${name}"]`).first().click({ timeout: 2000 }).catch(() => {});
  await sleep(200);
  return ok;
}
async function selectV(s) { await s.page.evaluate(() => { const { g } = __G(); g.setSelectionCell(__cell('v')); }); await sleep(250); }

async function S4format(delay, mode) {
  wsDelay = delay;
  const docId = `s4b-${delay}-${mode}`;
  const s = await openDoc(docId);
  await waitSettled(s.page);
  const run = newRun('S4b-format', delay, mode, docId);
  const chain = [await snap(s)];
  await step(s, chain, 'add', () => { const { g, p } = __G(); __R.v = g.insertVertex(p, null, 'Fmt', 300, 200, 160, 70, 'rounded=1;whiteSpace=wrap;html=1;').id; });
  const applied = [];
  // a) input edits in Style/Text/Arrange tabs (first visible text input, value + Enter, then blur on canvas)
  const plan = [['Style', '55'], ['Text', '19'], ['Arrange', '187']];
  for (const [tab, val] of plan) {
    await selectV(s);
    const found = await fmtTab(s, tab);
    const inp = s.frame.locator('.geFormatContainer input[type="text"]:visible, .geFormatContainer input:not([type]):visible').first();
    let done = false;
    if (found && (await inp.count())) {
      await inp.click({ timeout: 2000 }).catch(() => {});
      await inp.fill(val).catch(() => {});
      await inp.press('Enter').catch(() => {});
      await sleep(150);
      done = true;
    }
    await focusEditor(s); await waitSettled(s.page);
    const a = await snap(s);
    const changed = a.lh !== chain.at(-1).lh;
    if (changed) { a.label = `input:${tab}`; chain.push(a); }
    applied.push(`${tab}=${done ? (changed ? 'changed' : 'nochange') : 'notfound'}`);
  }
  // b) Format panel buttons: Bold + Italic in Text tab, Rounded/Shadow checkbox in Style tab
  for (const [tab, sel] of [['Text', '[title^="Bold"]'], ['Text', '[title^="Italic"]'], ['Style', 'input[type="checkbox"]']]) {
    await selectV(s);
    await fmtTab(s, tab);
    const loc = s.frame.locator(`.geFormatContainer ${sel}:visible`).first();
    let done = false;
    if (await loc.count()) { await loc.click({ timeout: 2000 }).catch(() => {}); done = true; await sleep(150); }
    await focusEditor(s); await waitSettled(s.page);
    const a = await snap(s);
    const changed = a.lh !== chain.at(-1).lh;
    if (changed) { a.label = `btn:${sel}`; chain.push(a); }
    applied.push(`${sel.replace(/[[\]"^=]/g, '')}=${done ? (changed ? 'changed' : 'nochange') : 'notfound'}`);
  }
  run.formatApplied = applied;
  run.chainLen = chain.length - 1;
  // probe: undo pressed while a Format input has focus (should not graph-undo or should undo exactly one)
  {
    await selectV(s); await fmtTab(s, 'Arrange');
    const inp = s.frame.locator('.geFormatContainer input[type="text"]:visible, .geFormatContainer input:not([type]):visible').first();
    const b = await snap(s);
    if (await inp.count()) { await inp.click({ timeout: 2000 }).catch(() => {}); await s.page.keyboard.press(s.undoKey); await sleep(150); }
    await focusEditor(s); await waitSettled(s.page);
    const a = await snap(s);
    const idx = chain.findIndex((c) => c.lh === a.lh);
    // draw.io re-applies the (unchanged) input value on blur -> a no-op history entry; the walk tolerates exactly that many
    run.allowNoop = Math.max(0, a.histLen - b.histLen);
    run.probes.undoInFocusedInput = { graphUndoCalls: a.undoCalls - b.undoCalls, stateIdx: idx, top: chain.length - 1, noopEntriesAdded: run.allowNoop, pass: idx === chain.length - 1 || idx === chain.length - 2 };
    run.probeCur = idx;
  }
  const top = chain.length - 1;
  let cur = run.probeCur >= 0 ? run.probeCur : top;
  cur = await undoToZero(s, chain, run, mode, top, cur, { extra: 2 });
  await checkpoint(s, chain, run, 'blank', 0);
  cur = await redoToTop(s, chain, run, mode, top, cur, { extra: 1 });
  await checkpoint(s, chain, run, 'redoTop', top);
  await finish(s, run);
}

// ---------------------------------------------------------------- S4c multi-page
async function S4pages(delay, mode) {
  wsDelay = delay;
  const docId = `s4c-${delay}-${mode}`;
  const s = await openDoc(docId);
  await waitSettled(s.page);
  const run = newRun('S4c-pages', delay, mode, docId);
  const chain = [await snap(s)];
  const sel = (key) => async () => { await s.page.evaluate((k) => { const { ui } = __G(); const pg = ui.getPageById(__R[k]); ui.selectPage(pg); }, key); await waitSettled(s.page); };
  await step(s, chain, 'p1:add', () => { const { g, p } = __G(); __R.v1 = g.insertVertex(p, null, 'P1', 80, 80, 120, 60).id; });
  await s.page.evaluate(() => { __R.p1 = __G().ui.currentPage.getId(); });
  await step(s, chain, 'insertPage2', () => { const { ui } = __G(); const pg = ui.insertPage(ui.createPage('Page-2', ui.createPageId())); __R.p2 = pg.getId(); });
  await step(s, chain, 'insertPage3', () => { const { ui } = __G(); const pg = ui.insertPage(ui.createPage('Page-3', ui.createPageId())); __R.p3 = pg.getId(); });
  await step(s, chain, 'renamePage2', () => { const { ui, W } = __G(); ui.editor.graph.model.execute(new W.RenamePage(ui, ui.getPageById(__R.p2), 'Renamed-2')); });
  await step(s, chain, 'renamePage3', () => { const { ui, W } = __G(); ui.editor.graph.model.execute(new W.RenamePage(ui, ui.getPageById(__R.p3), 'Third')); });
  await sel('p2')();
  run.onPage2 = await s.page.evaluate(() => __G().ui.currentPage.getId() === __R.p2);
  await step(s, chain, 'p2:addA', () => { const { g, p } = __G(); __R.a = g.insertVertex(p, null, 'A2', 60, 60, 120, 60).id; });
  await step(s, chain, 'p2:addB', () => { const { g, p } = __G(); __R.b = g.insertVertex(p, null, 'B2', 300, 60, 120, 60).id; });
  await step(s, chain, 'p2:edge', () => { const { g, p } = __G(); __R.e = g.insertEdge(p, null, 'ab2', __cell('a'), __cell('b')).id; });
  await step(s, chain, 'p2:fill', () => { const { g } = __G(); g.setCellStyles('fillColor', '#f8cecc', [__cell('a')]); });
  await step(s, chain, 'p2:deleteB', () => { const { g } = __G(); g.removeCells([__cell('b')], true); });
  await sel('p1')();
  await step(s, chain, 'deletePage2', () => { const { ui } = __G(); ui.removePage(ui.getPageById(__R.p2)); });
  run.pagesBeforeDelete = chain.at(-2).pages; run.pagesAfterDelete = chain.at(-1).pages;
  run.chainLen = chain.length - 1;
  await focusEditor(s);
  const top = chain.length - 1;
  // undo page delete
  let cur = await walkOne(s, chain, run, 'U', mode, top, top, 0);
  const f1 = await checkpoint(s, chain, run, 'undoPageDelete', top - 1);
  run.pagesAfterUndoDelete = f1.pages;
  // switch back to (restored) page 2 — this is itself an undoable SelectPage entry in draw.io
  await sel('p2')();
  run.backOnPage2 = await s.page.evaluate(() => { const { ui } = __G(); return ui.currentPage.getId() === __R.p2; });
  await focusEditor(s);
  // undo edits on page 2 and everything else back to blank
  const redoTop = top - 1; // the SelectPage above truncated the redo stack (the page-delete redo is gone by design)
  cur = await undoToZero(s, chain, run, mode, redoTop, cur, { extra: 2 });
  await checkpoint(s, chain, run, 'blank', 0);
  cur = await redoToTop(s, chain, run, mode, redoTop, cur, { extra: 1 });
  await checkpoint(s, chain, run, 'redoTop', redoTop);
  await finish(s, run);
}

// ---------------------------------------------------------------- S4d sidebar shapes
const PAL = { Misc: '^Misc$', UML: '^UML$', ER: '^Entity Relation', Flowchart: '^Flowchart$' };
async function S4sidebar(delay, mode) {
  wsDelay = delay;
  const docId = `s4d-${delay}-${mode}`;
  const s = await openDoc(docId);
  await waitSettled(s.page);
  const run = newRun('S4d-sidebar', delay, mode, docId);
  const chain = [await snap(s)];
  run.palettes = {};
  for (const [n, re] of Object.entries(PAL)) {
    const f = await s.page.evaluate(([name, rx]) => {
      const doc = document.getElementById('editor').contentDocument;
      const t = [...doc.querySelectorAll('.geSidebarContainer .geTitle')].find((x) => new RegExp(rx, 'i').test(x.textContent.trim()));
      if (!t) return null;
      t.setAttribute('data-ms-pal', name);
      t.scrollIntoView();
      const nodes = [...doc.querySelectorAll('.geSidebarContainer .geTitle, .geSidebarContainer .geItem')];
      let vis = 0;
      for (let i = nodes.indexOf(t) + 1; i < nodes.length && !nodes[i].classList.contains('geTitle'); i++) if (nodes[i].offsetParent !== null) vis++;
      return { vis };
    }, [n, re]);
    if (!f) { run.palettes[n] = 'NOT-FOUND'; continue; }
    if (f.vis === 0) { await s.frame.locator(`[data-ms-pal="${n}"]`).click({ timeout: 3000 }).catch(() => {}); await sleep(700); }
    const tags = await s.page.evaluate((name) => {
      const doc = document.getElementById('editor').contentDocument;
      const t = doc.querySelector(`[data-ms-pal="${name}"]`);
      const nodes = [...doc.querySelectorAll('.geSidebarContainer .geTitle, .geSidebarContainer .geItem')];
      const own = [];
      for (let i = nodes.indexOf(t) + 1; i < nodes.length && !nodes[i].classList.contains('geTitle'); i++) if (nodes[i].offsetParent !== null) own.push(nodes[i]);
      // spread picks across the palette (first, middle-ish, later)
      const picks = [0, Math.floor(own.length / 3), Math.floor((2 * own.length) / 3)].filter((v, i, a) => own[v] && a.indexOf(v) === i);
      return picks.map((ix, i) => { own[ix].setAttribute('data-ms-item', `${name}-${i}`); return `${name}-${i}|${(own[ix].getAttribute('title') || '').slice(0, 24)}`; });
    }, n);
    const got = [];
    for (const tg of tags) {
      const [tag, title] = tg.split('|');
      await s.page.evaluate(() => __G().g.clearSelection());
      await s.frame.locator(`[data-ms-item="${tag}"]`).click({ timeout: 3000 }).catch(() => {});
      await sleep(150); await waitSettled(s.page);
      const a = await snap(s);
      if (a.lh !== chain.at(-1).lh) { a.label = `${tag}:${title}`; chain.push(a); got.push(title || tag); } else got.push(`NOADD:${title}`);
    }
    run.palettes[n] = got;
  }
  run.chainLen = chain.length - 1;
  await s.page.evaluate(() => __G().g.clearSelection());
  await focusEditor(s);
  const top = chain.length - 1;
  let cur = await undoToZero(s, chain, run, mode, top, top, { extra: 2 });
  await checkpoint(s, chain, run, 'blank', 0);
  cur = await redoToTop(s, chain, run, mode, top, cur, { extra: 1 });
  await checkpoint(s, chain, run, 'redoTop', top);
  await finish(s, run);
}

// ---------------------------------------------------------------- main
async function safe(name, fn) {
  try { await fn(); }
  catch (e) { ERRORS.push(`${name}: ${String(e).slice(0, 240)}`); console.log(`ERROR in ${name}:`, String(e).slice(0, 240)); }
}

const SCN = { S1, S2, S3, S4inline, S4format, S4pages, S4sidebar };
const t0 = Date.now();
try {
  for (const [name, fn] of Object.entries(SCN)) {
    if (ONLY && !ONLY.includes(name)) continue;
    console.log(`== ${name} ==`);
    for (const d of DELAYS) for (const m of MODES) await safe(`${name} d=${d} ${m}`, () => fn(d, m));
  }
} finally {
  const sum = {};
  for (const r of RUNS) {
    const k = r.scn;
    sum[k] = sum[k] || { runs: 0, pass: 0, ok: 0, END: 0, PAGESEL: 0, NATIVE_NOOP: 0, NOOP: 0, MULTI: 0, LAG: 0, UNKNOWN: 0, nacks: 0, resyncs: 0, hashBad: 0, throws: 0, mismatch: 0, unconverged: 0, fallback: 0, badChecks: 0, badProbes: 0 };
    const x = sum[k];
    x.runs++; if (r.pass) x.pass++;
    for (const c of Object.keys(r.counts)) x[c] += r.counts[c];
    x.nacks += r.nacks; x.resyncs += r.resyncs; x.hashBad += r.hashBad; x.throws += r.throws; x.mismatch += r.mismatch; x.fallback += r.fallback;
    x.unconverged += (r.finalConverged ? 0 : 1) + Object.values(r.checks).filter((c) => !c.converged).length;
    x.badChecks += Object.values(r.checks).filter((c) => !c.atExpected).length;
    x.badProbes += Object.values(r.probes).filter((p) => p.pass === false).length;
  }
  console.log('\n== SUMMARY (per scenario, summed over delays x modes) ==');
  for (const [k, x] of Object.entries(sum)) console.log(`${k}: ${JSON.stringify(x)}`);
  const extra = RUNS.map((r) => ({ scn: r.scn, d: r.delay, m: r.mode, extra: { formatApplied: r.formatApplied, palettes: r.palettes, probes: r.probes, maxPendingDuringBurst: r.maxPendingDuringBurst, pendingAtFirstUndo: r.pendingAtFirstUndo, maxPendingDuringUndo: r.maxPendingDuringUndo, pages: [r.pagesBeforeDelete, r.pagesAfterDelete, r.pagesAfterUndoDelete], backOnPage2: r.backOnPage2, cells: [r.cellsTop, r.cellsAfterDelete] } }));
  for (const e of extra) {
    const v = Object.fromEntries(Object.entries(e.extra).filter(([, val]) => val !== undefined && !(Array.isArray(val) && val.every((z) => z === undefined))));
    if (Object.keys(v).length) console.log(`  detail ${e.scn} d=${e.d} ${e.m}: ${JSON.stringify(v).slice(0, 600)}`);
  }
  const uniq = [...new Set(LOGS)];
  console.log(`\nerrors=${ERRORS.length} totalRuns=${RUNS.length} passRuns=${RUNS.filter((r) => r.pass).length} consoleWarnErr=${uniq.length} elapsed=${Math.round((Date.now() - t0) / 1000)}s`);
  for (const e of ERRORS) console.log('  ERR', e);
  console.log(`toolbar: lateClicks=${TB.lateClicks} clickErrors=${TB.clickErrors.length}`);
  for (const e of [...new Set(TB.clickErrors)].slice(0, 8)) console.log('  TBERR', e);
  for (const l of uniq.slice(0, 15)) console.log('  LOG', l);
  fs.writeFileSync(OUT, JSON.stringify({ summary: sum, runs: RUNS, errors: ERRORS, logs: uniq }, null, 2));
  console.log('full details:', OUT);
  if (ERRORS.length || RUNS.some(r => !r.pass) || !RUNS.length) process.exitCode = 1;
  await browser.close();
  server.close();
}
