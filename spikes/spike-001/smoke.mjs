// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-001): harness smoke test.
import { chromium } from 'playwright-core';
import { start } from './server.mjs';
import { CHROME, freshPage, openEditor, op, canonOf } from './common.mjs';

const port = 8791;
const server = await start(port);
const browser = await chromium.launch({ executablePath: CHROME, headless: true });
const ctx = await browser.newContext();
try {
  const { page, consoleMsgs } = await freshPage(ctx, port);
  await openEditor(page, 'A');
  const info = await page.evaluate(() => {
    const w = H.frames.A.contentWindow;
    return { version: w.EditorUi.VERSION, mx: w.mxClient.VERSION, csp: !!w.document.querySelector('meta[http-equiv="Content-Security-Policy"]'), pages: H.ui.A.pages.length };
  });
  console.log('info', info);
  const r = await op(page, 'A', 'move', 'X', 10, 5);
  console.log('autosave', JSON.stringify(r.autosaves.map((a) => ({ patch: a.patch, checksum: a.checksum, hasXml: 'xml' in a }))));
  console.log('cap', JSON.stringify(await page.evaluate(() => CAP.A)));
  console.log('canon cells', (await canonOf(page, 'A'))[0].cells.length);
  console.log('console errors', consoleMsgs.filter((m) => m.type === 'error' || m.type === 'pageerror').slice(0, 10));
} finally {
  await browser.close();
  server.close();
}
