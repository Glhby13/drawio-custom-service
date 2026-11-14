import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
const require = createRequire(new URL('../package.json', import.meta.url));
const { chromium } = require('playwright-core');
import { startServer } from './repro-server.mjs';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const { server, port, docs } = await startServer(0, { wsDelayMs: () => 0 });
const browser = await chromium.launch({
  executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
});

async function openClient(docId, user) {
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  const logs = [];
  page.on('console', (m) => { if (m.type() === 'error' && !/404/.test(m.text())) logs.push(m.text()); });
  page.on('pageerror', (e) => logs.push(String(e)));
  await page.goto(`http://127.0.0.1:${port}/d/${docId}?user=${user}`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && window.__adapter.editorUi(), null, { timeout: 30000 });
  const frame = page.frameLocator('#editor');
  await frame.locator('.geSidebarContainer .geItem').first().waitFor({ timeout: 20000 });
  const undoKey = process.platform === 'darwin' ? 'Meta+z' : 'Control+z';
  const redoKey = process.platform === 'darwin' ? 'Meta+Shift+z' : 'Control+y';
  return { ctx, page, user, undoKey, redoKey, logs };
}

async function settle(clients, minSeq) {
  for (const c of clients) {
    const t0 = Date.now();
    while (Date.now() - t0 < 10000) {
      const ok = await c.page.evaluate((ms) => {
        const s = window.__adapter.state();
        return s.joined && !s.busy && s.pending === 0 && s.seq >= ms;
      }, minSeq);
      if (ok) break;
      await sleep(50);
    }
  }
  await sleep(100);
}

async function inspect(c) {
  return c.page.evaluate(async () => {
    const a = window.__adapter, ui = a.editorUi(), g = ui.editor.graph, um = ui.editor.undoManager;
    const shapes = Object.values(g.getModel().cells)
      .filter((x) => x.vertex || x.edge)
      .map((x) => String(x.value))
      .sort();
    return {
      seq: a.state().seq,
      shapes,
      histLen: um.history.length,
      nextAdd: um.indexOfNextAdd,
      lh: await a.liveCanonHash(),
      ch: await a.modelCanonHash('confirmed'),
    };
  });
}

try {
  const docId = 'collab-abc-undo';
  const A = await openClient(docId, 'userA');
  const B = await openClient(docId, 'userB');
  const C = await openClient(docId, 'userC');
  const all = [A, B, C];
  await settle(all, 0);

  // 1. User A inserts Shape-A
  await A.page.evaluate(() => {
    const g = window.__adapter.editorUi().editor.graph;
    g.insertVertex(g.getDefaultParent(), null, 'Shape-A', 60, 60, 120, 60, 'rounded=1;whiteSpace=wrap;html=1;');
  });
  await settle(all, 1);

  // 2. User B inserts Shape-B
  await B.page.evaluate(() => {
    const g = window.__adapter.editorUi().editor.graph;
    g.insertVertex(g.getDefaultParent(), null, 'Shape-B', 240, 60, 120, 60, 'rounded=1;whiteSpace=wrap;html=1;');
  });
  await settle(all, 2);

  // 3. User C inserts Shape-C
  await C.page.evaluate(() => {
    const g = window.__adapter.editorUi().editor.graph;
    g.insertVertex(g.getDefaultParent(), null, 'Shape-C', 420, 60, 120, 60, 'rounded=1;whiteSpace=wrap;html=1;');
  });
  await settle(all, 3);

  const beforeUndo = await Promise.all(all.map(inspect));
  for (const x of beforeUndo) { assert.deepEqual(x.shapes, ['Shape-A', 'Shape-B', 'Shape-C']); assert.equal(x.lh, x.ch); }
  console.log('BEFORE B Ctrl+Z:', JSON.stringify(beforeUndo.map((x, i) => ({ user: all[i].user, shapes: x.shapes, histLen: x.histLen, nextAdd: x.nextAdd })), null, 2));

  // 4. User B presses Ctrl+Z (Cmd+Z on macOS headless Chromium)
  await B.page.locator('#editor').click({ position: { x: 700, y: 400 } });
  await B.page.keyboard.press(B.undoKey);
  await settle(all, 4);

  const afterBUndo = await Promise.all(all.map(inspect));
  for (const x of afterBUndo) { assert.deepEqual(x.shapes, ['Shape-A', 'Shape-C']); assert.equal(x.lh, x.ch); }
  console.log('AFTER B Ctrl+Z:', JSON.stringify(afterBUndo.map((x, i) => ({ user: all[i].user, shapes: x.shapes, histLen: x.histLen, nextAdd: x.nextAdd, converged: x.lh === x.ch })), null, 2));

  // 5. User B presses Redo (Ctrl+Y / Cmd+Shift+Z)
  await B.page.keyboard.press(B.redoKey);
  await settle(all, 5);

  const afterBRedo = await Promise.all(all.map(inspect));
  for (const x of afterBRedo) { assert.deepEqual(x.shapes, ['Shape-A', 'Shape-B', 'Shape-C']); assert.equal(x.lh, x.ch); }
  console.log('AFTER B Redo:', JSON.stringify(afterBRedo.map((x, i) => ({ user: all[i].user, shapes: x.shapes, histLen: x.histLen, nextAdd: x.nextAdd, converged: x.lh === x.ch })), null, 2));

  // 6. Verify if User B presses Ctrl+Z twice, does it stop or touch Shape-A?
  await B.page.keyboard.press(B.undoKey);
  await settle(all, 6);
  await B.page.keyboard.press(B.undoKey);
  await sleep(300);
  const afterBSecondUndo = await Promise.all(all.map(inspect));
  for (const x of afterBSecondUndo) { assert.deepEqual(x.shapes, ['Shape-A', 'Shape-C']); assert.equal(x.lh, x.ch); }
  console.log('AFTER B 2x Ctrl+Z:', JSON.stringify(afterBSecondUndo.map((x, i) => ({ user: all[i].user, shapes: x.shapes, histLen: x.histLen, nextAdd: x.nextAdd, converged: x.lh === x.ch })), null, 2));

  assert.equal(docs.get(docId).nacks.length, 0);
  console.log('PASS A/B/C selective local undo + redo + empty-stack boundary; no server rejections');
  await Promise.all(all.map((c) => c.ctx.close()));
} finally {
  await browser.close();
  server.close();
  server.closeAllConnections?.();
}
