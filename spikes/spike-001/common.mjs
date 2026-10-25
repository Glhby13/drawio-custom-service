// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-001): shared helpers for run.mjs.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
export const INPAGE = fs.readFileSync(path.join(here, 'inpage.js'), 'utf8');
// CHROME=bundled -> use the browser shipped with Playwright (needed inside the Playwright container)
export const CHROME = process.env.CHROME === 'bundled' ? undefined : (process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome');

// Base document: page p1 with X, Y, Z, container C and edge E (X->Y); page p2 with W.
export const BASE_XML = `<mxfile><diagram id="p1" name="Page-1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/>
<mxCell id="X" value="X" style="rounded=0;whiteSpace=wrap;html=1;" vertex="1" parent="1"><mxGeometry x="40" y="40" width="120" height="60" as="geometry"/></mxCell>
<mxCell id="Y" value="Y" style="rounded=0;whiteSpace=wrap;html=1;" vertex="1" parent="1"><mxGeometry x="240" y="40" width="120" height="60" as="geometry"/></mxCell>
<mxCell id="Z" value="Z" style="ellipse;whiteSpace=wrap;html=1;" vertex="1" parent="1"><mxGeometry x="40" y="200" width="80" height="80" as="geometry"/></mxCell>
<mxCell id="C" value="C" style="swimlane;container=1;" vertex="1" parent="1"><mxGeometry x="300" y="200" width="300" height="200" as="geometry"/></mxCell>
<mxCell id="E" style="edgeStyle=orthogonalEdgeStyle;" edge="1" parent="1" source="X" target="Y"><mxGeometry relative="1" as="geometry"/></mxCell>
</root></mxGraphModel></diagram><diagram id="p2" name="Page-2"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/>
<mxCell id="W" value="W" style="rounded=0;" vertex="1" parent="1"><mxGeometry x="40" y="40" width="120" height="60" as="geometry"/></mxCell>
</root></mxGraphModel></diagram></mxfile>`;

export function genXml(n) {
  // n cells: 80% vertices on a grid, 20% edges between consecutive vertices.
  const nv = Math.max(2, Math.round(n * 0.8)), ne = n - nv;
  const parts = ['<mxfile><diagram id="p1" name="Page-1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/>'];
  for (let i = 0; i < nv; i++) {
    const x = (i % 50) * 140, y = Math.floor(i / 50) * 90;
    parts.push(`<mxCell id="v${i}" value="Node ${i}" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#dae8fc;strokeColor=#6c8ebf;" vertex="1" parent="1"><mxGeometry x="${x}" y="${y}" width="120" height="60" as="geometry"/></mxCell>`);
  }
  for (let i = 0; i < ne; i++) {
    parts.push(`<mxCell id="e${i}" style="edgeStyle=orthogonalEdgeStyle;rounded=0;html=1;" edge="1" parent="1" source="v${i}" target="v${(i + 1) % nv}"><mxGeometry relative="1" as="geometry"/></mxCell>`);
  }
  parts.push('</root></mxGraphModel></diagram></mxfile>');
  return parts.join('');
}

export async function freshPage(ctx, port) {
  const page = await ctx.newPage();
  const consoleMsgs = [];
  page.on('console', (m) => consoleMsgs.push({ type: m.type(), text: m.text() }));
  page.on('pageerror', (e) => consoleMsgs.push({ type: 'pageerror', text: String(e) }));
  await page.goto(`http://localhost:${port}/`);
  await page.addScriptTag({ content: INPAGE });
  return { page, consoleMsgs };
}

// Opens editor `name`, loads xml with autosave + diffSync patchOnly, attaches EditorUi, starts change capture.
export async function openEditor(page, name, xml = BASE_XML, opts = {}) {
  await page.evaluate(async ({ name, extra }) => { await H.open(name, extra); }, { name, extra: opts.extra || '' });
  await page.evaluate(async ({ name, xml, diffSync, auto }) => {
    const p = H.wait(name, 'load', null, 60000);
    H.send(name, { action: 'load', xml, autosave: auto, diffSync });
    await p;
    await H.attach(name);
    capture(name);
  }, { name, xml, diffSync: opts.diffSync === undefined ? { patchOnly: true } : opts.diffSync, auto: opts.autosave === undefined ? 1 : opts.autosave });
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Runs an op in the page and returns the autosave messages the editor emitted for it.
export async function op(page, name, fn, ...args) {
  return page.evaluate(async ({ name, fn, args }) => {
    const before = H.log.length;
    const ret = OPS[fn](name, ...args);
    await new Promise((r) => setTimeout(r, 60));
    const autosaves = H.log.slice(before).filter((e) => e.frame === name && e.data.event === 'autosave').map((e) => e.data);
    return { ret: ret === undefined ? null : ret, autosaves };
  }, { name, fn, args });
}

// Applies a native patch to editor `name` via the embed 'patch' action, returns the ack event.
export async function applyPatch(page, name, patch) {
  return page.evaluate(async ({ name, patch }) => {
    const before = H.log.length;
    const t0 = performance.now();
    const p = H.wait(name, 'patch', null, 30000);
    H.send(name, { action: 'patch', patch });
    const ack = await p;
    const dt = performance.now() - t0;
    await new Promise((r) => setTimeout(r, 60));
    const echoes = H.log.slice(before).filter((e) => e.frame === name && e.data.event === 'autosave').length;
    return { ack: ack.data, dt, echoes };
  }, { name, patch });
}

export const canonOf = (page, name) => page.evaluate((n) => canon(n), name);
export const checksumOf = (page, name) => page.evaluate((n) => checksum(n), name);
export const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
