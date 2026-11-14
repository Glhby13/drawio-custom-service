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

function connectRawWs(docId) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`ws://127.0.0.1:${port}/ws/v1/documents/${docId}`);
    ws.addEventListener('open', () => ws.send(JSON.stringify({ t: 'join', since: 0 })));
    ws.addEventListener('message', (ev) => {
      const m = JSON.parse(ev.data);
      if (m.t === 'joined') resolve(ws);
    });
    ws.addEventListener('error', reject);
  });
}

try {
  const docId = 'rootonly-page-1';
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await ctx.newPage();
  const logs = [];
  page.on('console', (m) => { if (m.type() === 'error' && !/404/.test(m.text())) logs.push(m.text()); });
  page.on('pageerror', (e) => logs.push(String(e)));

  await page.goto(`http://127.0.0.1:${port}/d/${docId}?user=alice`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && window.__adapter.editorUi(), null, { timeout: 30000 });
  const frame = page.frameLocator('#editor');
  await frame.locator('.geSidebarContainer .geItem').first().waitFor({ timeout: 20000 });

  // Raw WS client sends a valid root-only page (addPage p2 + addCell '0', NO layer '1')
  const raw = await connectRawWs(docId);
  const acked = new Promise((resolve) => {
    raw.addEventListener('message', (ev) => {
      const m = JSON.parse(ev.data);
      if (m.t === 'ack' && m.batch === 'b_rootonly') resolve(m);
      if (m.t === 'nack' && m.batch === 'b_rootonly') resolve(m);
    });
  });
  raw.send(JSON.stringify({
    t: 'ops',
    batch: 'b_rootonly',
    base: 0,
    ops: [
      { id: 'r1', t: 'addPage', page: 'p2', name: 'RootOnly', order: '1', attrs: '' },
      { id: 'r2', t: 'addCell', page: 'p2', cell: '0', kind: '-', parent: '', order: '', props: {} },
    ],
  }));
  const ackMsg = await acked;
  console.log('raw ackMsg:', ackMsg);

  // Immediately check (< 200ms) vs after bounded recovery (wait up to 2000ms)
  await sleep(80);
  const early = await page.evaluate(() => ({
    pages: window.__adapter.editorUi().pages.map((p) => p.getId()),
    tPages: Array.from(window.__adapter.confirmed().pages.keys()),
  }));
  console.log('early (~80ms):', early);

  let after = null;
  const t0 = Date.now();
  while (Date.now() - t0 < 3000) {
    after = await page.evaluate(async () => ({
      pages: window.__adapter.editorUi().pages.map((p) => p.getId()),
      tPages: Array.from(window.__adapter.confirmed().pages.keys()),
      stats: window.__adapter.stats,
      lh: await window.__adapter.liveCanonHash(),
      ch: await window.__adapter.modelCanonHash('confirmed'),
    }));
    if (after.pages.includes('p2')) break;
    await sleep(40);
  }
  const releaseMs = Date.now() - t0;
  // Once draw.io renders the root-only page, flush any local layer normalization and verify zero NACKs & full convergence
  await page.evaluate(() => window.__adapter.flush());
  await sleep(120);
  const settled = await page.evaluate(async () => ({
    pages: window.__adapter.editorUi().pages.map((p) => p.getId()),
    tPages: Array.from(window.__adapter.confirmed().pages.keys()),
    stats: window.__adapter.stats,
    lh: await window.__adapter.liveCanonHash(),
    ch: await window.__adapter.modelCanonHash('confirmed'),
  }));
  console.log(`released in ${releaseMs}ms:`, after.pages);
  console.log('settled:', settled);
  console.log('server nacks:', docs.get(docId).nacks);
  console.log('browser logs:', logs);

  const ok = ackMsg.t === 'ack'
    && !early.pages.includes('p2')
    && after.pages.length === 2
    && after.pages.includes('p2')
    && releaseMs >= 150 && releaseMs < 1500
    && settled.stats.nacked === 0
    && docs.get(docId).nacks.length === 0
    && settled.lh === settled.ch
    && logs.length === 0;
  console.log('RESULT:', ok ? 'PASS' : 'FAIL');
  await ctx.close();
  raw.close();
  process.exitCode = ok ? 0 : 1;
} finally {
  await browser.close();
  server.close();
  server.closeAllConnections?.();
}
