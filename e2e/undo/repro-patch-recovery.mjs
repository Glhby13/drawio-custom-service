import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const require = createRequire(new URL('../package.json', import.meta.url));
const { chromium } = require('playwright-core');
import { startServer } from './repro-server.mjs';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const { server, port, docs } = await startServer(0, { wsDelayMs: () => 0 });
const browser = await chromium.launch({
  executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
});

async function openClient(docId, user, withPatchInterceptor = false) {
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  const logs = [];
  page.on('console', (m) => {
    if (m.type() === 'error') {
      const t = m.text();
      if (!/404/.test(t)) logs.push(`[${user}][error] ${t}`);
    }
  });
  page.on('pageerror', (e) => logs.push(`[${user}][pageerror] ${e}`));
  if (withPatchInterceptor) {
    await page.addInitScript(() => {
      if (window === window.top) {
        window.__injectMode = null;
        window.__intercepted = [];
        return;
      }
      window.addEventListener('message', (ev) => {
        const mode = window.top.__injectMode;
        if (!mode || ev.origin !== location.origin || ev.source !== window.parent) return;
        let msg;
        try { msg = JSON.parse(ev.data); } catch (e) { return; }
        if (msg.action !== 'patch') return;
        window.top.__injectMode = null;
        ev.stopImmediatePropagation();
        window.top.__intercepted.push({ mode, t: Date.now() });
        if (mode === 'inject_patch_error') {
          window.parent.postMessage(JSON.stringify({ event: 'patch', error: 'simulated_patch_error' }), location.origin);
        }
      }, true);
    });
  }
  await page.goto(`http://127.0.0.1:${port}/d/${docId}?user=${user}`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && window.__adapter.editorUi(), null, { timeout: 30000 });
  const frame = page.frameLocator('#editor');
  await page.waitForFunction(() => window.__adapter.editorUi().pages?.length && document.getElementById('status')?.dataset.state === 'ready', null, { timeout: 60000 });
  await page.evaluate(() => {
    window.__k = 0;
    window.__addBox = (lbl, x, y) => {
      const ui = window.__adapter.editorUi(), g = ui.editor.graph, p = g.getDefaultParent();
      return g.insertVertex(p, null, lbl, x, y, 120, 60, 'rounded=1;whiteSpace=wrap;html=1;');
    };
  });
  return { ctx, page, logs };
}

async function waitSettled(c, minSeq = 0, timeout = 10000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeout) {
    const st = await c.page.evaluate(() => {
      const a = window.__adapter, s = a.state();
      return { joined: s.joined, pending: s.pending, busy: s.busy, seq: s.seq, patches: a.stats.patches, resyncs: a.stats.resyncs };
    });
    if (st.joined && !st.busy && st.pending === 0 && st.seq >= minSeq) return st;
    await sleep(60);
  }
  throw new Error('timeout waiting settled');
}

async function snap(c) {
  return c.page.evaluate(async () => {
    const a = window.__adapter, ui = a.editorUi(), g = ui.editor.graph;
    const cells = Object.values(g.getModel().cells).filter((x) => x.vertex || x.edge)
      .map((x) => String(x.value)).sort();
    return {
      seq: a.state().seq,
      resyncs: a.stats.resyncs,
      patches: a.stats.patches,
      nacked: a.stats.nacked,
      lh: await a.liveCanonHash(),
      ch: await a.modelCanonHash('confirmed'),
      cells,
    };
  });
}

try {
  const docId = 'patch-recovery-1';
  const alice = await openClient(docId, 'alice', true);
  const bob = await openClient(docId, 'bob', false);
  await waitSettled(alice, 0);
  await waitSettled(bob, 0);

  // Baseline edit from Alice
  await alice.page.evaluate(() => window.__addBox('A1', 40, 40));
  await sleep(100);
  await waitSettled(alice, 1);
  await waitSettled(bob, 1);

  const docRec = docs.get(docId);

  // 1) Inject patch_error on Alice when Bob adds B1
  const opCountBeforeErr = docRec.opLog.length;
  const aliceResyncsBeforeErr = (await snap(alice)).resyncs;
  await alice.page.evaluate(() => { window.__injectMode = 'inject_patch_error'; });
  await bob.page.evaluate(() => window.__addBox('B1', 220, 40));
  await waitSettled(bob, 2);
  // Wait for Alice to hit patch_error, resync, reconnect, and reload full state
  const tErr0 = Date.now();
  while (Date.now() - tErr0 < 8000) {
    const sa = await snap(alice);
    if (sa.resyncs > aliceResyncsBeforeErr && sa.seq >= 2 && sa.lh === sa.ch) break;
    await sleep(60);
  }
  await sleep(150);
  const aliceAfterErr = await snap(alice);
  const bobAfterErr = await snap(bob);
  assert.ok(aliceAfterErr.resyncs > aliceResyncsBeforeErr, 'patch error triggers resync');
  assert.equal(aliceAfterErr.lh, aliceAfterErr.ch);
  assert.equal(aliceAfterErr.lh, bobAfterErr.lh);
  const opsAddedDuringErr = docRec.opLog.slice(opCountBeforeErr);
  const aliceInterceptErr = await alice.page.evaluate(() => window.__intercepted.slice());

  console.log('== Patch Error Recovery ==');
  console.log(JSON.stringify({
    elapsedMs: Date.now() - tErr0,
    intercepted: aliceInterceptErr,
    aliceResyncs: `${aliceResyncsBeforeErr} -> ${aliceAfterErr.resyncs}`,
    aliceCells: aliceAfterErr.cells,
    bobCells: bobAfterErr.cells,
    convergedAliceBob: aliceAfterErr.lh === bobAfterErr.lh && aliceAfterErr.lh === aliceAfterErr.ch,
    opsByAliceDuringRecovery: opsAddedDuringErr.filter((o) => o.actor?.id === 'alice' || o.actor === 'alice').length,
    totalOpsDuringErr: opsAddedDuringErr.map((o) => ({ t: o.t, seq: o.seq })),
    serverNacks: docRec.nacks.length,
  }, null, 2));

  // 2) Inject missing patch ack (timeout after 4000ms) on Alice when Bob adds B2
  const opCountBeforeTo = docRec.opLog.length;
  const aliceResyncsBeforeTo = aliceAfterErr.resyncs;
  await alice.page.evaluate(() => { window.__injectMode = 'drop_next_patch_ack'; });
  await bob.page.evaluate(() => window.__addBox('B2', 400, 40));
  await waitSettled(bob, aliceAfterErr.seq + 1);
  const tTo0 = Date.now();
  while (Date.now() - tTo0 < 10000) {
    const sa = await snap(alice);
    if (sa.resyncs > aliceResyncsBeforeTo && sa.seq >= bobAfterErr.seq + 1 && sa.lh === sa.ch) break;
    await sleep(100);
  }
  await sleep(150);
  const aliceAfterTo = await snap(alice);
  const aliceAdapterState = await alice.page.evaluate(() => ({ state: window.__adapter.state(), injectMode: window.__injectMode, intercepted: window.__intercepted }));
  const bobAfterTo = await snap(bob);
  assert.ok(aliceAfterTo.resyncs > aliceResyncsBeforeTo, 'patch timeout triggers resync');
  assert.equal(aliceAfterTo.lh, aliceAfterTo.ch);
  assert.equal(aliceAfterTo.lh, bobAfterTo.lh);
  const opsAddedDuringTo = docRec.opLog.slice(opCountBeforeTo);
  const aliceInterceptTo = await alice.page.evaluate(() => window.__intercepted.slice());

  console.log('== Patch Timeout Recovery ==');
  console.log(JSON.stringify({
    elapsedMs: Date.now() - tTo0,
    intercepted: aliceInterceptTo,
    aliceAdapterState,
    serverSessions: docRec.sessions.size,
    aliceResyncs: `${aliceResyncsBeforeTo} -> ${aliceAfterTo.resyncs}`,
    aliceCells: aliceAfterTo.cells,
    bobCells: bobAfterTo.cells,
    convergedAliceBob: aliceAfterTo.lh === bobAfterTo.lh && aliceAfterTo.lh === aliceAfterTo.ch,
    totalOpsDuringTo: opsAddedDuringTo.map((o) => ({ t: o.t, seq: o.seq })),
    serverNacks: docRec.nacks.length,
  }, null, 2));

  // 3) Verify Alice can still edit & undo normally after both resyncs
  await alice.page.evaluate(() => window.__addBox('A2', 40, 160));
  await sleep(120);
  await waitSettled(alice, aliceAfterTo.seq + 1);
  await alice.page.evaluate(() => window.__adapter.editorUi().actions.get('undo').funct());
  await sleep(120);
  await waitSettled(alice, aliceAfterTo.seq + 2);
  await waitSettled(bob, aliceAfterTo.seq + 2);
  const aliceFinal = await snap(alice);
  const bobFinal = await snap(bob);
  assert.equal(aliceFinal.lh, bobFinal.lh);
  assert.equal(aliceFinal.lh, aliceFinal.ch);
  assert.equal(docRec.nacks.length, 0);
  console.log('== Post-Recovery Edit + Undo ==');
  console.log(JSON.stringify({
    aliceCells: aliceFinal.cells,
    bobCells: bobFinal.cells,
    converged: aliceFinal.lh === bobFinal.lh && aliceFinal.lh === aliceFinal.ch,
    serverNacks: docRec.nacks.length,
    logs: [...alice.logs, ...bob.logs],
  }, null, 2));

  await alice.ctx.close();
  await bob.ctx.close();
} finally {
  await browser.close();
  server.close();
}
