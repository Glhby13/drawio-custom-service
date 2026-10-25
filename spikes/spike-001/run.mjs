// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-001): all experiments, results -> results/.
import fs from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';
import { start } from './server.mjs';
import { CHROME, BASE_XML, genXml, freshPage, openEditor, op, applyPatch, canonOf, checksumOf, same, sleep } from './common.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const outDir = path.join(here, 'results');
fs.mkdirSync(outDir, { recursive: true });
const only = (process.env.ONLY || '').split(',').filter(Boolean);
const want = (k) => only.length === 0 || only.includes(k);

const port = Number(process.env.GW_PORT || 8790), portPlugins = Number(process.env.GW_PORT_PLUGINS || 8793);
const tag = process.env.RUN_TAG || 'local';
const server = await start(port);
const serverPlugins = await start(portPlugins, { allowPlugins: true });
const browser = await chromium.launch({ executablePath: CHROME, headless: true });
const R = { startedAt: new Date().toISOString(), env: {}, experiments: {} };
const save = () => fs.writeFileSync(path.join(outDir, `results-${tag}.json`), JSON.stringify(R, null, 2));

async function withPage(fn, p = port) {
  const ctx = await browser.newContext();
  try { const fp = await freshPage(ctx, p); return await fn(fp.page, fp.consoleMsgs); } finally { await ctx.close(); }
}

try {
  // ---------------------------------------------------------------- E0 environment, subpath, CSP
  if (want('E0')) R.experiments.E0 = await withPage(async (page, msgs) => {
    const failed = [];
    page.on('response', (r) => { if (r.status() >= 400) failed.push(`${r.status()} ${new URL(r.url()).pathname}`); });
    await openEditor(page, 'A');
    const info = await page.evaluate(async (port) => {
      const w = H.frames.A.contentWindow;
      const violations = [];
      w.document.addEventListener('securitypolicyviolation', (e) => violations.push({ directive: e.violatedDirective, blocked: e.blockedURI }));
      const tryFetch = async (u) => { try { const r = await w.fetch(u); return `ok ${r.status}`; } catch (e) { return 'blocked/failed'; } };
      const sameOrigin = await tryFetch('/api/ping');
      const crossOrigin = await tryFetch(`http://127.0.0.1:${port}/api/ping`);
      let ws = 'n/a';
      try { const s = new w.WebSocket(`ws://localhost:${port}/ws`); ws = 'constructed'; s.close(); } catch (e) { ws = 'threw ' + e.name; }
      await new Promise((r) => setTimeout(r, 300));
      return { drawioVersion: w.EditorUi.VERSION, ua: navigator.userAgent, cspMeta: w.document.querySelector('meta[http-equiv="Content-Security-Policy"]')?.getAttribute('content')?.slice(0, 80) + '...',
        editorPath: w.location.pathname, sameOriginFetch: sameOrigin, crossOriginFetch: crossOrigin, websocketSameOrigin: ws, violations };
    }, port);
    return { ...info, httpErrors: [...new Set(failed)], consoleErrors: msgs.filter((m) => m.type === 'error' || m.type === 'pageerror').map((m) => m.text).filter((t, i, a) => a.indexOf(t) === i).slice(0, 15) };
  });
  save();

  // ---------------------------------------------------------------- E1 plugin loading paths
  if (want('E1')) {
    const e1 = {};
    e1.hostInjectedLoadPlugin = await withPage(async (page) => {
      await openEditor(page, 'A');
      return page.evaluate(() => ({ attached: !!H.ui.A, hasGraph: !!(H.ui.A && H.ui.A.editor.graph) }));
    });
    e1.urlParamP = await withPage(async (page, msgs) => {
      await page.evaluate(async () => { await H.open('A', '&p=/spike-plugin.js'); });
      await sleep(1500);
      const loaded = await page.evaluate(() => !!H.frames.A.contentWindow.__spikePluginLoaded);
      return { loaded, console: msgs.filter((m) => /plugin/i.test(m.text)).map((m) => m.text) };
    });
    const settingsPlugin = async (p) => withPage(async (page, msgs) => {
      await page.evaluate(() => { localStorage.setItem('.drawio-config', JSON.stringify({ language: '', configVersion: null, customFonts: [], libraries: 'general', customLibraries: ['L.scratchpad'], plugins: ['/spike-plugin.js'], recentColors: [], formatWidth: '240', createTarget: false, pageFormat: { x: 0, y: 0, width: 850, height: 1100 }, search: true, showStartScreen: true, gridColor: '#d0d0d0', darkGridColor: '#424242', autosave: true, resizeImages: null, openCounter: 0, version: 18, unit: 1, isRulerOn: false, ui: '' })); });
      await page.evaluate(async () => { await H.open('A'); });
      await sleep(1500);
      const loaded = await page.evaluate(() => !!H.frames.A.contentWindow.__spikePluginLoaded);
      return { loaded, console: msgs.filter((m) => /plugin/i.test(m.text)).map((m) => m.text) };
    }, p);
    e1.settingsPluginDefaultConfig = await settingsPlugin(port);
    e1.settingsPluginAllowCustom = await settingsPlugin(portPlugins);
    R.experiments.E1 = e1;
    save();
  }

  // ---------------------------------------------------------------- E2 change capture per operation type
  const OPS_E2 = [
    ['move', ['X', 10, 5]], ['resize', ['X', 200]], ['label', ['X', 'Hello']], ['style', ['X', 'fillColor', '#ff0000']],
    ['attr', ['X', 'owner', 'alice']], ['add', ['N1', 60, 320]], ['connect', ['E2', 'X', 'Z']], ['disconnect', ['E']],
    ['group', [['X', 'Y']]], ['reparent', ['Z', 'C']], ['toBack', ['Y']], ['del', ['Z']], ['addPage', ['Page-3']],
    ['renamePage', [1, 'Renamed']], ['selectPage', [1]],
  ];
  if (want('E2')) {
    const rows = [];
    for (const [fn, args] of OPS_E2) {
      rows.push(await withPage(async (page) => {
        await openEditor(page, 'A');
        await page.evaluate(() => { CAP.A.length = 0; });
        const r = await op(page, 'A', fn, ...args);
        const cap = await page.evaluate(() => CAP.A);
        const patches = r.autosaves.map((a) => a.patch).filter(Boolean);
        const xmlOnly = r.autosaves.filter((a) => !a.patch && a.xml).length;
        return { op: fn, args, changeTypes: [...new Set(cap.flat().map((c) => c.type))], changes: cap.flat(),
          autosaveCount: r.autosaves.length, xmlOnlyAutosaves: xmlOnly, patches, patchBytes: patches.reduce((s, p) => s + JSON.stringify(p).length, 0) };
      }));
    }
    R.experiments.E2 = rows;
    save();
  }

  // ---------------------------------------------------------------- E3 replay + undo replay
  if (want('E3')) R.experiments.E3 = await withPage(async (page) => {
    await openEditor(page, 'A');
    await openEditor(page, 'B');
    const seq = [['move', ['X', 30, 0]], ['label', ['Y', 'Y2']], ['style', ['Z', 'fillColor', '#00ff00']], ['add', ['N1', 60, 320]],
      ['connect', ['E2', 'N1', 'Z']], ['group', [['X', 'Y']]], ['reparent', ['Z', 'C']], ['toBack', ['C']], ['addPage', ['P3']], ['selectPage', [0]], ['del', ['N1']], ['undo', []], ['undo', []]];
    const patches = [];
    for (const [fn, args] of seq) { const r = await op(page, 'A', fn, ...args); for (const a of r.autosaves) patches.push({ fn, patch: a.patch || null, hasXml: !!a.xml }); }
    const results = [];
    for (const p of patches) if (p.patch) results.push((await applyPatch(page, 'B', p.patch)).ack.checksumMismatch || false);
    const [ca, cb] = [await canonOf(page, 'A'), await canonOf(page, 'B')];
    return { ops: seq.map((s) => s[0]), autosaves: patches.length, withoutPatch: patches.filter((p) => !p.patch).length,
      replayEqual: same(ca, cb), checksumA: await checksumOf(page, 'A'), checksumB: await checksumOf(page, 'B') };
  });
  save();

  // ---------------------------------------------------------------- E4 remote apply side effects
  if (want('E4')) R.experiments.E4 = await withPage(async (page) => {
    await openEditor(page, 'A');
    await openEditor(page, 'B');
    await op(page, 'B', 'label', 'Y', 'local-edit');               // B has one local undoable edit
    await page.evaluate(() => { const g = H.ui.B.editor.graph; g.setSelectionCell(g.model.getCell('Z')); });
    const before = await page.evaluate(() => ({ undo: H.ui.B.editor.undoManager.history.length, next: H.ui.B.editor.undoManager.indexOfNextAdd,
      page: H.ui.B.currentPage.getId(), sel: H.ui.B.editor.graph.getSelectionCells().map((c) => c.id) }));
    const r = await op(page, 'A', 'move', 'X', 100, 0);
    const ap = await applyPatch(page, 'B', r.autosaves[0].patch);
    const after = await page.evaluate(() => ({ undo: H.ui.B.editor.undoManager.history.length, next: H.ui.B.editor.undoManager.indexOfNextAdd,
      page: H.ui.B.currentPage.getId(), sel: H.ui.B.editor.graph.getSelectionCells().map((c) => c.id), xX: H.ui.B.editor.graph.model.getCell('X').geometry.x }));
    const dup = await applyPatch(page, 'B', r.autosaves[0].patch);    // duplicate delivery
    const afterDup = await page.evaluate(() => H.ui.B.editor.graph.model.getCell('X').geometry.x);
    await op(page, 'B', 'undo');                                      // must undo the local label, not the remote move
    const afterUndo = await page.evaluate(() => ({ label: H.ui.B.editor.graph.model.getCell('Y').value, xX: H.ui.B.editor.graph.model.getCell('X').geometry.x }));
    // remote delete of the cell selected in B
    await page.evaluate(() => { const g = H.ui.B.editor.graph; g.setSelectionCell(g.model.getCell('Z')); });
    const rd = await op(page, 'A', 'del', 'Z');
    const apDel = await applyPatch(page, 'B', rd.autosaves[0].patch);
    const selAfterDel = await page.evaluate(() => H.ui.B.editor.graph.getSelectionCells().map((c) => c.id));
    // remote change while B is on another page
    await page.evaluate(() => OPS.selectPage('B', 1));
    const rp = await op(page, 'A', 'label', 'X', 'while-B-on-p2');
    const apPage = await applyPatch(page, 'B', rp.autosaves[0].patch);
    const pageAfter = await page.evaluate(() => H.ui.B.currentPage.getId());
    return { before, after, echoesOnApply: ap.echoes, checksumMatch: !ap.ack.checksumMismatch && ap.ack.checksum === r.autosaves[0].checksum,
      duplicate: { echoes: dup.echoes, xBefore: after.xX, xAfter: afterDup, error: dup.ack.error || null },
      afterLocalUndo: afterUndo, remoteDeleteOfSelected: { error: apDel.ack.error || null, selection: selAfterDel },
      remoteWhileOtherPage: { error: apPage.ack.error || null, currentPage: pageAfter, echoes: apPage.echoes },
      applyLatencyMs: ap.dt };
  });
  save();

  // ---------------------------------------------------------------- E5 conflict matrix
  // Every scenario: A ops then B ops (concurrently, before any exchange). Server order = all of A, then all of B
  // (offline scenario: B first). Strategy S1 = naive relay (each client applies the other's patches).
  // Strategy S3 = server-sequenced: replica R applies in server order; each client walks the global order,
  // own entries are acks, a remote entry is applied and then still-unacked own patches are re-applied.
  const SC = [
    { id: 'move-move-same', a: [['move', ['X', 50, 0]]], b: [['move', ['X', 0, 80]]], check: null },
    { id: 'move-resize-same-cell', a: [['move', ['X', 50, 0]]], b: [['resize', ['X', 300]]], check: (c) => ({ moved: geo(c, 'X').x === 90, resized: geo(c, 'X').w === 300 }) },
    { id: 'label-label-same', a: [['label', ['X', 'A-label']]], b: [['label', ['X', 'B-label']]], check: null },
    { id: 'style-diff-keys-same-cell', a: [['style', ['X', 'fillColor', '#ff0000']]], b: [['style', ['X', 'strokeColor', '#0000ff']]], check: (c) => ({ fillKept: /fillColor=#ff0000/.test(cellOf(c, 'X').style), strokeKept: /strokeColor=#0000ff/.test(cellOf(c, 'X').style) }) },
    { id: 'style-same-key', a: [['style', ['X', 'fillColor', '#ff0000']]], b: [['style', ['X', 'fillColor', '#00ff00']]], check: null },
    { id: 'attr-diff-keys-same-cell', a: [['attr', ['X', 'k1', 'a']]], b: [['attr', ['X', 'k2', 'b']]], check: (c) => ({ k1Kept: /k1="a"/.test(cellOf(c, 'X').value || ''), k2Kept: /k2="b"/.test(cellOf(c, 'X').value || '') }) },
    { id: 'delete-vs-edit', a: [['del', ['X']]], b: [['label', ['X', 'edited']]], check: (c) => ({ xExists: !!cellOf(c, 'X') }) },
    { id: 'edit-vs-delete', a: [['label', ['X', 'edited']]], b: [['del', ['X']]], check: (c) => ({ xExists: !!cellOf(c, 'X') }) },
    { id: 'group-vs-move', a: [['group', [['X', 'Y']]]], b: [['move', ['X', 0, 100]]], check: (c) => ({ xParent: cellOf(c, 'X')?.parent, xGeo: geo(c, 'X') }) },
    { id: 'reorder-diff-cells', a: [['toBack', ['X']]], b: [['toFront', ['Y']]], check: (c) => ({ order: c[0].cells.filter((x) => x.parent === '1').map((x) => x.id).join(',') }) },
    { id: 'reorder-same-cell', a: [['toBack', ['X']]], b: [['toFront', ['X']]], check: (c) => ({ order: c[0].cells.filter((x) => x.parent === '1').map((x) => x.id).join(',') }) },
    { id: 'addpage-addpage', a: [['addPage', ['PA']]], b: [['addPage', ['PB']]], check: (c) => ({ pages: c.map((p) => p.name).join(',') }) },
    { id: 'reparent-vs-move', a: [['reparent', ['X', 'C']]], b: [['move', ['X', 0, 100]]], check: (c) => ({ xParent: cellOf(c, 'X')?.parent, xGeo: geo(c, 'X') }) },
    { id: 'reparent-vs-delete-parent', a: [['reparent', ['X', 'C']]], b: [['del', ['C']]], check: (c) => ({ xExists: !!cellOf(c, 'X'), cExists: !!cellOf(c, 'C'), xParent: cellOf(c, 'X')?.parent || null }) },
    { id: 'diff-cells', a: [['move', ['X', 50, 0]]], b: [['label', ['Y', 'B-label']]], check: (c) => ({ moved: geo(c, 'X').x === 90, labelled: cellOf(c, 'Y').value === 'B-label' }) },
    { id: 'add-add', a: [['add', ['NA', 60, 320]]], b: [['add', ['NB', 200, 320]]], check: (c) => ({ na: !!cellOf(c, 'NA'), nb: !!cellOf(c, 'NB') }) },
    { id: 'connect-vs-delete-target', a: [['connect', ['E2', 'X', 'Z']]], b: [['del', ['Z']]], check: (c) => ({ e2: cellOf(c, 'E2') ? { source: cellOf(c, 'E2').source, target: cellOf(c, 'E2').target } : null, z: !!cellOf(c, 'Z') }) },
    { id: 'offline-reconnect', bFirst: true, a: [['move', ['X', 50, 0]], ['label', ['Y', 'A-off']], ['style', ['Z', 'fillColor', '#ff0000']]], b: [['label', ['X', 'B-on']], ['del', ['Z']]], check: (c) => ({ xMoved: geo(c, 'X')?.x === 90, xLabel: cellOf(c, 'X')?.value, yLabel: cellOf(c, 'Y')?.value, z: !!cellOf(c, 'Z') }) },
  ];
  function cellOf(c, id) { for (const p of c) for (const x of p.cells) if (x.id === id) return x; return null; }
  function geo(c, id) { const x = cellOf(c, id); if (!x || !x.geometry) return null; const m = (k) => { const r = new RegExp(`${k}="([-0-9.]+)"`).exec(x.geometry); return r ? Number(r[1]) : 0; }; return { x: m('x'), y: m('y'), w: m('width'), h: m('height') }; }

  // repair: after S3, a client whose checksum differs from R pulls R's XML and converges either with the
  // embed 'merge' action or with an adapter-computed diff (ui.diffPages(own, R)) sent through 'patch'.
  async function repair(page, client, method) {
    return page.evaluate(async ({ client, method }) => {
      const xml = H.ui.R.getXmlForPages(H.ui.R.pages);
      if (method === 'merge') { const p = H.wait(client, 'merge', null, 30000); H.send(client, { action: 'merge', xml }); await p; }
      else {
        const ui = H.ui[client];
        const patch = ui.diffPages(ui.pages, ui.getPagesForXml(xml));
        const p = H.wait(client, 'patch', null, 30000); H.send(client, { action: 'patch', patch }); await p;
      }
      return checksum(client) === checksum('R');
    }, { client, method });
  }

  async function runScenario(sc, strategy, repairMethod) {
    return withPage(async (page) => {
      for (const n of ['A', 'B', 'R']) await openEditor(page, n);
      const pa = [], pb = [];
      for (const [fn, args] of sc.a) for (const s of (await op(page, 'A', fn, ...args)).autosaves) if (s.patch) pa.push(s.patch);
      for (const [fn, args] of sc.b) for (const s of (await op(page, 'B', fn, ...args)).autosaves) if (s.patch) pb.push(s.patch);
      const order = sc.bFirst ? [...pb.map((p) => ['B', p]), ...pa.map((p) => ['A', p])] : [...pa.map((p) => ['A', p]), ...pb.map((p) => ['B', p])];
      const errors = [];
      const ap = async (n, p) => { const r = await applyPatch(page, n, p); if (r.ack.error) errors.push(`${n}: ${r.ack.error}`); return r; };
      for (const [, p] of order) await ap('R', p);                    // reference replica, server order
      if (strategy === 'S1') {
        for (const p of pb) await ap('A', p);
        for (const p of pa) await ap('B', p);
      } else {
        for (const client of ['A', 'B']) {
          const pending = (client === 'A' ? pa : pb).slice();
          for (const [origin, p] of order) {
            if (origin === client) pending.shift();
            else { await ap(client, p); for (const own of pending) await ap(client, own); }
          }
        }
      }
      const repaired = {};
      if (repairMethod) for (const c of ['A', 'B']) if (await checksumOf(page, c) !== await checksumOf(page, 'R')) repaired[c] = await repair(page, c, repairMethod);
      const [ca, cb, cr] = [await canonOf(page, 'A'), await canonOf(page, 'B'), await canonOf(page, 'R')];
      return { repairMethod: repairMethod || null, repaired, patches: { a: pa.length, b: pb.length }, convergedAB: same(ca, cb), aEqualsR: same(ca, cr), bEqualsR: same(cb, cr),
        checksums: { A: await checksumOf(page, 'A'), B: await checksumOf(page, 'B'), R: await checksumOf(page, 'R') },
        resultR: sc.check ? sc.check(cr) : null, resultA: sc.check ? sc.check(ca) : null, resultB: sc.check ? sc.check(cb) : null,
        errors, patchSample: { a: pa[0] || null, b: pb[0] || null } };
    });
  }
  if (want('E5')) {
    const rows = [];
    for (const sc of SC) {
      const row = { id: sc.id };
      for (const s of ['S1', 'S3']) { try { row[s] = await runScenario(sc, s); } catch (e) { row[s] = { failed: String(e) }; } }
      rows.push(row);
      R.experiments.E5 = rows;
      save();
      process.stdout.write(`E5 ${sc.id} S1 conv=${row.S1.convergedAB} S3 conv=${row.S3.convergedAB}/R=${row.S3.aEqualsR && row.S3.bEqualsR}\n`);
    }
  }

  if (want('E5R')) {
    const rows = [];
    for (const sc of SC.filter((s) => ['add-add', 'addpage-addpage', 'reparent-vs-delete-parent', 'move-resize-same-cell'].includes(s.id))) {
      const row = { id: sc.id };
      for (const m of ['merge', 'adapterDiff']) { try { row[m] = await runScenario(sc, 'S3', m); } catch (e) { row[m] = { failed: String(e) }; } }
      rows.push(row);
      R.experiments.E5R = rows;
      save();
    }
  }

  // ---------------------------------------------------------------- E6 payload + latency benchmark
  if (want('E6')) {
    const rows = [];
    for (const n of [50, 500, 5000]) {
      rows.push(await withPage(async (page) => {
        const xml = genXml(n);
        const t0 = Date.now();
        await openEditor(page, 'A', xml);
        const loadA = Date.now() - t0;
        await openEditor(page, 'B', xml);
        const samples = [];
        for (let i = 0; i < 5; i++) {
          const t = await page.evaluate(async () => {
            const before = H.log.length, s = performance.now();
            const p = H.wait('A', 'autosave', null, 60000);
            OPS.move('A', 'v1', 5, 0);
            const ev = await p;
            return { opToAutosaveMs: ev.t - s, patch: ev.data.patch, checksum: ev.data.checksum, idx: before };
          });
          const ap = await applyPatch(page, 'B', t.patch);
          samples.push({ opToAutosaveMs: t.opToAutosaveMs, applyMs: ap.dt, patchBytes: JSON.stringify(t.patch).length, checksumOk: !ap.ack.checksumMismatch });
        }
        const sem = JSON.stringify({ operationId: '018f6c1e-6a1b-7c3d-9e4f-0a1b2c3d4e5f', documentId: 'd-123', clientSequence: 42, baseServerSequence: 1000,
          type: 'setGeometry', payload: { pageId: 'p1', cellId: 'v1', x: 145, y: 0, width: 120, height: 60 } });
        // full-document merge for comparison
        const m = await page.evaluate(async (xml2) => {
          const s = performance.now(); const p = H.wait('B', 'merge', null, 120000);
          H.send('B', { action: 'merge', xml: xml2 }); await p; return performance.now() - s;
        }, xml.replace('Node 3"', 'Node 3 merged"'));
        const med = (k) => { const v = samples.map((x) => x[k]).sort((a, b) => a - b); return Math.round(v[Math.floor(v.length / 2)] * 10) / 10; };
        return { cells: n, fullXmlBytes: xml.length, fullXmlDeflateBytes: zlib.deflateRawSync(Buffer.from(xml)).length, loadMs: loadA,
          nativePatchBytes: samples[0].patchBytes, semanticOpBytes: sem.length, medianOpToAutosaveMs: med('opToAutosaveMs'), medianPatchApplyMs: med('applyMs'),
          fullMergeApplyMs: Math.round(m), checksumsOk: samples.every((s) => s.checksumOk), samples };
      }));
      R.experiments.E6 = rows;
      save();
      process.stdout.write(`E6 ${n} done\n`);
    }
  }

  // ---------------------------------------------------------------- E8 in-place text edit while a remote patch arrives
  if (want('E8')) {
    const variants = [
      ['remote-geometry-same-cell', async (page) => (await op(page, 'A', 'move', 'X', 60, 0)).autosaves[0].patch],
      ['remote-label-same-cell', async (page) => (await op(page, 'A', 'label', 'X', 'remote-label')).autosaves[0].patch],
      ['remote-label-other-cell', async (page) => (await op(page, 'A', 'label', 'Y', 'remote-Y')).autosaves[0].patch],
      ['remote-delete-same-cell', async (page) => (await op(page, 'A', 'del', 'X')).autosaves[0].patch],
    ];
    const rows = [];
    for (const [name, mk] of variants) {
      rows.push(await withPage(async (page) => {
        await openEditor(page, 'A'); await openEditor(page, 'B');
        const patch = await mk(page);
        const started = await page.evaluate(() => {
          const g = H.ui.B.editor.graph; g.setSelectionCell(g.model.getCell('X')); g.startEditingAtCell(g.model.getCell('X'));
          const ed = g.cellEditor; if (!g.isEditing()) return { editing: false };
          ed.textarea.innerHTML = 'typed-by-B';
          return { editing: true, textareaBefore: ed.textarea.innerHTML };
        });
        const ap = await applyPatch(page, 'B', patch);
        const mid = await page.evaluate(() => { const g = H.ui.B.editor.graph; return { stillEditing: g.isEditing(), textarea: g.isEditing() ? g.cellEditor.textarea.innerHTML : null }; });
        const end = await page.evaluate(() => {
          const g = H.ui.B.editor.graph; let err = null;
          try { g.stopEditing(false); } catch (e) { err = String(e); }
          const c = g.model.getCell('X');
          return { error: err, xExists: !!c, value: c ? (c.value && c.value.nodeType ? 'xml:' + c.value.getAttribute('label') : c.value) : null, x: c && c.geometry ? c.geometry.x : null };
        });
        const echoes = await page.evaluate(() => H.log.filter((e) => e.frame === 'B' && e.data.event === 'autosave').length);
        return { variant: name, started, patchError: ap.ack.error || null, afterPatch: mid, afterStopEditing: end, autosavesFromB: echoes };
      }));
    }
    R.experiments.E8 = rows;
    save();
  }

  // ---------------------------------------------------------------- E9 undo of a local edit after remote patches touched the same cell
  if (want('E9')) {
    const rows = [];
    // U1 remote geometry, local label undone; U2 remote label overwrites local label, then local undo; U3 remote delete of a cell A added, then A undoes
    rows.push(await withPage(async (page) => {
      await openEditor(page, 'A'); await openEditor(page, 'B');
      await op(page, 'A', 'label', 'X', 'A1');
      const rb = await op(page, 'B', 'move', 'X', 70, 0);
      await applyPatch(page, 'A', rb.autosaves[0].patch);
      const u = await op(page, 'A', 'undo');
      const st = await page.evaluate(() => { const c = H.ui.A.editor.graph.model.getCell('X'); return { label: c.value, x: c.geometry.x }; });
      return { case: 'U1 local label, remote move, local undo', undoAutosaves: u.autosaves.length, result: st, expected: { label: 'X', x: 110 } };
    }));
    rows.push(await withPage(async (page) => {
      await openEditor(page, 'A'); await openEditor(page, 'B');
      await op(page, 'A', 'label', 'X', 'A1');
      const rb = await op(page, 'B', 'label', 'X', 'B1');
      await applyPatch(page, 'A', rb.autosaves[0].patch);
      const before = await page.evaluate(() => H.ui.A.editor.graph.model.getCell('X').value);
      const u = await op(page, 'A', 'undo');
      const after = await page.evaluate(() => H.ui.A.editor.graph.model.getCell('X').value);
      return { case: 'U2 local label A1, remote label B1, local undo', beforeUndo: before, afterUndo: after, undoPatch: u.autosaves.map((a) => a.patch) };
    }));
    rows.push(await withPage(async (page) => {
      await openEditor(page, 'A'); await openEditor(page, 'B');
      const add = await op(page, 'A', 'add', 'N1', 60, 320);
      await applyPatch(page, 'B', add.autosaves[0].patch);
      const del = await op(page, 'B', 'del', 'N1');
      await applyPatch(page, 'A', del.autosaves[0].patch);
      let err = null, u = null;
      try { u = await op(page, 'A', 'undo'); } catch (e) { err = String(e); }
      const st = await page.evaluate(() => ({ n1: !!H.ui.A.editor.graph.model.getCell('N1') }));
      return { case: 'U3 local add N1, remote delete N1, local undo', error: err, n1ExistsInA: st.n1, undoAutosaves: u ? u.autosaves.length : null, undoPatch: u ? u.autosaves.map((a) => a.patch) : null };
    }));
    R.experiments.E9 = rows;
    save();
  }

  // ---------------------------------------------------------------- E10 where does the per-change time go? (draw.io internals, same page)
  if (want('E10')) {
    const rows = [];
    for (const n of [50, 500, 5000]) {
      rows.push(await withPage(async (page) => {
        await openEditor(page, 'A', genXml(n));
        return page.evaluate(async (n) => {
          const ui = H.ui.A, g = ui.editor.graph;
          const med = (fn, k = 7) => { const v = []; for (let i = 0; i < k; i++) { const t = performance.now(); fn(); v.push(performance.now() - t); } v.sort((a, b) => a - b); return Math.round(v[k >> 1] * 100) / 100; };
          const shadow = ui.clonePages(ui.pages), cur = ui.clonePages(ui.pages);
          const out = { cells: n };
          out.clonePagesMs = med(() => ui.clonePages(ui.pages));
          out.hashPagesMs = med(() => ui.getHashValueForPages(ui.pages));
          out.diffPagesNoChangeMs = med(() => ui.diffPages(shadow, cur));
          out.xmlSerializeMs = med(() => ui.getXmlForPages(ui.pages));
          out.xmlBytes = ui.getXmlForPages(ui.pages).length;
          const v1 = g.model.getCell('v1');
          out.modelMoveOnlyMs = med(() => { g.model.beginUpdate(); try { const geo = v1.geometry.clone(); geo.x += 1; g.model.setGeometry(v1, geo); } finally { g.model.endUpdate(); } });
          // full cycle observed by the host (op -> autosave event)
          const cyc = [];
          for (let i = 0; i < 5; i++) { const t = performance.now(); const p = H.wait('A', 'autosave', null, 60000); OPS.move('A', 'v1', 1, 0); const ev = await p; cyc.push(ev.t - t); }
          cyc.sort((a, b) => a - b); out.fullCycleOpToAutosaveMs = Math.round(cyc[2] * 10) / 10;
          return out;
        }, n)
          .then(async (out) => {
            // same document, autosave/diffSync OFF: the pure draw.io graph update cost of one geometry change
            await openEditor(page, 'C', genXml(n), { autosave: 0, diffSync: false });
            out.modelMoveNoAutosaveMs = await page.evaluate(() => {
              const g = H.ui.C.editor.graph, v1 = g.model.getCell('v1'), v = [];
              for (let i = 0; i < 7; i++) { const t = performance.now(); g.model.beginUpdate(); try { const geo = v1.geometry.clone(); geo.x += 1; g.model.setGeometry(v1, geo); } finally { g.model.endUpdate(); } v.push(performance.now() - t); }
              v.sort((a, b) => a - b); return Math.round(v[3] * 100) / 100;
            });
            return out;
          });
      }));
      R.experiments.E10 = rows;
      save();
    }
  }

  // ---------------------------------------------------------------- E11 air-gapped browser: every non-localhost request is blocked and recorded
  if (want('E11')) {
    const ctx2 = await browser.newContext();
    const blocked = new Set(), allowed = new Set();
    await ctx2.route('**/*', (route) => {
      const u = new URL(route.request().url());
      if (u.hostname === 'localhost' || u.hostname === '127.0.0.1') { allowed.add(u.pathname.split('/').slice(0, 3).join('/')); return route.continue(); }
      blocked.add(u.origin); return route.abort();
    });
    const fp = await freshPage(ctx2, port);
    const t0 = Date.now();
    await openEditor(fp.page, 'A'); await openEditor(fp.page, 'B');
    const a = await op(fp.page, 'A', 'move', 'X', 10, 0); await applyPatch(fp.page, 'B', a.autosaves[0].patch);
    await op(fp.page, 'A', 'add', 'N1', 60, 320); await op(fp.page, 'A', 'label', 'N1', 'offline-label');
    const [ca, cb] = [await canonOf(fp.page, 'A'), await canonOf(fp.page, 'B')];
    const consoleNet = fp.consoleMsgs.filter((m) => /ERR_|Failed to load|blocked/i.test(m.text)).map((m) => m.text).slice(0, 8);
    R.experiments.E11 = { blockedOrigins: [...blocked], editorReadyAndEditingWorksMs: Date.now() - t0, patchApplied: !!cb[0].cells.find((c) => c.id === 'X'), moveReplicated: same(ca.map((p) => p.cells.find((c) => c.id === 'X')), cb.map((p) => p.cells.find((c) => c.id === 'X'))), consoleNetworkMessages: consoleNet };
    await ctx2.close();
    save();
  }

  // ---------------------------------------------------------------- E7 server-built patch (AI/MCP lowering) + hostile patch
  if (want('E7')) R.experiments.E7 = await withPage(async (page, msgs) => {
    await openEditor(page, 'A');
    const handBuilt = { u: { p1: { cells: { i: [{ id: 'AI1', parent: '1', vertex: 1, value: 'from-server', style: 'rounded=1;fillColor=#fff2cc;',
      geometry: '<mxGeometry x="420" y="40" width="100" height="40" as="geometry"/>' }], u: { Y: { value: 'renamed-by-ai' } } } } } };
    const r1 = await applyPatch(page, 'A', handBuilt);
    const c = await canonOf(page, 'A');
    const ai1 = c[0].cells.find((x) => x.id === 'AI1');
    const hostile = [
      { u: { p1: { cells: { u: { X: { __proto__: { polluted: 1 }, constructor: 'x', getId: 1 } } } } } },
      { u: { p1: { cells: { i: 'not-an-array' } } } },
      { u: { p1: { cells: { u: { X: null, Y: 7 } } } } },
      { u: { nopage: { cells: { u: { X: { value: 'ghost' } } } } } },
      { u: { p1: { cells: { i: [{ id: 'Q', parent: 'does-not-exist', vertex: 1, value: 'orphan' }] } } } },
    ];
    const hr = [];
    for (const h of hostile) { const r = await applyPatch(page, 'A', h); hr.push({ error: r.ack.error || null, mismatch: !!r.ack.checksumMismatch }); }
    const after = await canonOf(page, 'A');
    const polluted = await page.evaluate(() => ({}).polluted === 1 || H.frames.A.contentWindow.Object.prototype.polluted === 1);
    return { handBuilt: { error: r1.ack.error || null, inserted: !!ai1, yRenamed: c[0].cells.find((x) => x.id === 'Y').value },
      hostile: hr, prototypePolluted: polluted, orphanInserted: !!after[0].cells.find((x) => x.id === 'Q'), stillUsable: (await op(page, 'A', 'move', 'X', 1, 1)).autosaves.length > 0,
      consoleErrors: msgs.filter((m) => m.type === 'error').map((m) => m.text).filter((t) => !/Cyclic|404/.test(t)).slice(0, 8) };
  });
  save();
} finally {
  R.finishedAt = new Date().toISOString();
  save();
  await browser.close();
  server.close();
  serverPlugins.close();
}
console.log('results ->', path.join(outDir, `results-${tag}.json`));
