// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-002 §checksum).
// For every corpus document: hash(JS, from the LIVE draw.io model) == hash(Go, from raw XML) == hash(Go, from
// the XML draw.io itself exports). Corpus = hand-written nasty documents + random edit sequences on the live editor.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { chromium } from '../spike-001/node_modules/playwright-core/index.mjs';
import { start } from '../spike-001/server.mjs';
import { CHROME, BASE_XML, freshPage, openEditor } from '../spike-001/common.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);
const bin = process.env.CANON_BIN || '/tmp/spike003-canon';
const tag = process.env.RUN_TAG || 'local';
const port = Number(process.env.GW_PORT || 8795);
const RANDOM_DOCS = Number(process.env.RANDOM_DOCS || 150);
const outDir = path.join(here, 'results');
fs.mkdirSync(outDir, { recursive: true });

const wrap = (cells, pages = 1) => `<mxfile><diagram id="p1" name="Page-1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/>${cells}</root></mxGraphModel></diagram></mxfile>`;
const v = (id, label, extra = '', geo = '<mxGeometry x="10" y="20" width="100" height="40" as="geometry"/>') =>
  `<mxCell id="${id}" value="${label}" style="rounded=1;${extra}" vertex="1" parent="1">${geo}</mxCell>`;

const HAND = {
  basic: wrap(v('A', 'A') + v('B', 'B') + '<mxCell id="E" edge="1" parent="1" source="A" target="B"><mxGeometry relative="1" as="geometry"/></mxCell>'),
  unicode: wrap(v('u1', '日本語テキスト') + v('u2', 'emoji 😀🎉 ZWJ 👨&#x200d;👩&#x200d;👧') + v('u3', 'مرحبا بالعالم') + v('u4', 'e&#x301; vs &#xe9;') + v('u5', 'ΑΒΓ αβγ ñ ü ß')),
  entities: wrap(v('e1', '&amp;&lt;&gt;&quot;&#x27;') + v('e2', 'line1&#xa;line2') + v('e3', 'tab&#x9;here') + v('e4', 'pipe | and back\\slash') + v('e5', 'cr&#xd;lf&#xa;')),
  literalNewlineInAttr: wrap('<mxCell id="n1" value="a\nb\tc" style="rounded=1;" vertex="1" parent="1"><mxGeometry x="1" y="2" width="3" height="4" as="geometry"/></mxCell>'),
  emptyVsAbsentValue: wrap('<mxCell id="a" value="" vertex="1" parent="1"><mxGeometry width="10" height="10" as="geometry"/></mxCell><mxCell id="b" vertex="1" parent="1"><mxGeometry width="10" height="10" as="geometry"/></mxCell>'),
  emptyStyleVsAbsent: wrap('<mxCell id="a" value="x" style="" vertex="1" parent="1"><mxGeometry width="10" height="10" as="geometry"/></mxCell><mxCell id="b" value="x" vertex="1" parent="1"><mxGeometry width="10" height="10" as="geometry"/></mxCell>'),
  userObject: wrap('<UserObject label="Hello" owner="alice" empty="" id="uo1"><mxCell style="rounded=1;" vertex="1" parent="1"><mxGeometry x="5" y="5" width="50" height="50" as="geometry"/></mxCell></UserObject>'),
  objectPlaceholders: wrap('<object label="%name% &amp; %city%" placeholders="1" name="N" city="C" id="ob1"><mxCell style="whiteSpace=wrap;html=1;" vertex="1" parent="1"><mxGeometry x="5" y="5" width="50" height="50" as="geometry"/></mxCell></object>'),
  styleOrderA: wrap(v('s', 'x', 'fillColor=#ff0000;strokeColor=#00ff00;whiteSpace=wrap;')),
  styleOrderB: wrap(v('s', 'x', 'whiteSpace=wrap;strokeColor=#00ff00;fillColor=#ff0000')),
  styleDupAndNames: wrap('<mxCell id="s" value="x" style="ellipse;shape=cloud;a=1;a=2;;rounded=0;" vertex="1" parent="1"><mxGeometry width="10" height="10" as="geometry"/></mxCell>'),
  numbersDecimal: wrap(v('n', 'x', '', '<mxGeometry x="100.0" y="0050" width="1e2" height="0.03125" as="geometry"/>')),
  numbersTiny: wrap(v('n', 'x', '', '<mxGeometry x="1e-7" y="-0" width="12345678901234567890" height="0.1" as="geometry"/>')),
  numbersNonCanonical: wrap(v('n', 'x', '', '<mxGeometry x="5px" y="+5" width=" 7" height="8" as="geometry"/>')),
  geometryDefaultsExplicit: wrap('<mxCell id="g" value="x" vertex="1" parent="1"><mxGeometry x="0" y="0" width="0" height="0" relative="0" as="geometry"/></mxCell>'),
  multiPage: `<mxfile><diagram id="p1" name="One"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/>${v('a', 'a')}</root></mxGraphModel></diagram><diagram id="p2" name="Two | with \\ chars"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/>${v('b', 'b')}</root></mxGraphModel></diagram><diagram id="p3" name="Empty"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/></root></mxGraphModel></diagram></mxfile>`,
  groupsContainers: wrap('<mxCell id="grp" value="" style="group" vertex="1" connectable="0" parent="1"><mxGeometry x="40" y="40" width="200" height="100" as="geometry"/></mxCell><mxCell id="c1" value="in1" style="rounded=1;" vertex="1" parent="grp"><mxGeometry width="80" height="40" as="geometry"/></mxCell><mxCell id="c2" value="in2" style="rounded=1;" vertex="1" parent="grp"><mxGeometry x="100" y="50" width="80" height="40" as="geometry"/></mxCell>'),
  collapsedAlt: wrap('<mxCell id="col" value="C" style="swimlane;" vertex="1" collapsed="1" parent="1"><mxGeometry x="10" y="10" width="140" height="30" as="geometry"><mxRectangle x="10" y="10" width="200" height="120" as="alternateBounds"/></mxGeometry></mxCell>'),
  edgePoints: wrap(v('a', 'a') + v('b', 'b', '', '<mxGeometry x="300" y="200" width="50" height="50" as="geometry"/>') +
    '<mxCell id="e" edge="1" parent="1" source="a" target="b"><mxGeometry relative="1" as="geometry"><mxPoint x="1.5" y="2.5" as="sourcePoint"/><mxPoint x="20" y="30" as="targetPoint"/><Array as="points"><mxPoint x="120" y="20"/><mxPoint x="120" y="220"/></Array></mxGeometry></mxCell>' +
    '<mxCell id="el" value="label" style="edgeLabel;" vertex="1" connectable="0" parent="e"><mxGeometry x="0.2" y="5" relative="1" as="geometry"><mxPoint x="3" y="-4" as="offset"/></mxGeometry></mxCell>'),
  hiddenNonConnectable: wrap('<mxCell id="h" value="x" vertex="1" visible="0" connectable="0" parent="1"><mxGeometry width="5" height="5" as="geometry"/></mxCell>'),
  danglingTerminal: wrap(v('a', 'a') + '<mxCell id="e" edge="1" parent="1" source="a"><mxGeometry relative="1" as="geometry"><mxPoint x="50" y="50" as="targetPoint"/></mxGeometry></mxCell>'),
  htmlLabel: wrap(v('h', '&lt;b&gt;bold&lt;/b&gt;&lt;br&gt;second &amp;amp; third', 'html=1;whiteSpace=wrap;')),
  siblingOrder: wrap(v('z1', '1') + v('z2', '2') + v('z3', '3')),
  siblingOrderReversed: wrap(v('z3', '3') + v('z2', '2') + v('z1', '1')),
};

// ---- random edit sequences on the live editor -------------------------------------------------------------------
const RANDOM_JS = `
window.RND = function (seed) { let s = seed >>> 0; const r = () => { s = (Math.imul(s, 1664525) + 1013904223) >>> 0; return s / 4294967296; }; return r; };
window.randomEdits = function (name, seed, steps) {
  const r = RND(seed), pick = (a) => a[Math.floor(r() * a.length)];
  const ui = H.ui[name], g = ui.editor.graph, m = g.model;
  const labels = ['plain', 'Ünïcödé', '日本語', 'a|b', 'back\\\\slash', 'emoji 😀', 'x&y<z>', 'multi\\nline', '', ' spaced '];
  const styles = [['fillColor', '#ff0000'], ['strokeColor', '#0000ff'], ['rounded', '1'], ['fontSize', '14'], ['dashed', '1'], ['opacity', '50']];
  const cells = () => Object.values(m.cells).filter((c) => c.parent && c.parent.parent); // excludes root(0) & layers(parent=root)
  let n = 0;
  for (let i = 0; i < steps; i++) {
    const cs = cells(), vs = cs.filter((c) => c.vertex && !c.edge);
    try {
      switch (Math.floor(r() * 14)) {
        case 0: if (cs.length) g.moveCells([pick(cs)], Math.round(r() * 200 - 100) / 4, Math.round(r() * 200 - 100) / 4); break;
        case 1: if (vs.length) { const c = pick(vs), geo = c.geometry.clone(); geo.width = Math.round(20 + r() * 300) / 8; geo.height = Math.round(20 + r() * 200) / 8; m.setGeometry(c, geo); } break;
        case 2: if (cs.length) m.setValue(pick(cs), pick(labels)); break;
        case 3: if (cs.length) { const [k, val] = pick(styles); g.setCellStyles(k, val, [pick(cs)]); } break;
        case 4: if (cs.length) g.setAttributeForCell(pick(cs), pick(['owner', 'tag', 'team']), pick(labels)); break;
        case 5: case 6: g.insertVertex(g.getDefaultParent(), 'N' + seed + '_' + (n++), pick(labels), Math.round(r() * 800) / 4, Math.round(r() * 600) / 4, 80, 40, 'rounded=1;'); break;
        case 7: if (vs.length > 1) g.insertEdge(g.getDefaultParent(), 'E' + seed + '_' + (n++), pick(labels), pick(vs), pick(vs)); break;
        case 8: if (cs.length > 3) g.removeCells([pick(cs)], true); break;
        case 9: if (vs.length > 1) { const a = pick(vs), b = pick(vs); if (a !== b && a.parent === b.parent && a.parent === g.getDefaultParent()) g.groupCells(null, 0, [a, b]); } break;
        case 10: { const cont = cs.filter((c) => c.vertex && c.style && /group|swimlane/.test(c.style)); if (cont.length && vs.length) { const c = pick(vs), p = pick(cont); if (c !== p && !m.isAncestor(c, p)) m.add(p, c); } break; }
        case 11: if (cs.length) g.orderCells(r() < 0.5, [pick(cs)]); break;
        case 12: { const p = ui.createPage('Pg' + seed + '_' + (n++), ui.createPageId()); ui.insertPage(p); break; }
        case 13: ui.selectPage(pick(ui.pages)); break;
      }
    } catch (e) { /* invalid random op: skipped */ }
  }
  return true;
};
window.canonLive = async function (name) {
  const ui = H.ui[name];
  ui.pages.forEach((p) => ui.updatePageRoot(p));
  const pages = ui.pages.map((p) => ({ id: p.getId(), name: p.getName(), root: CANON.fromLive(p.root) }));
  const canon = CANON.canonical(pages);
  return { canon, hash: await CANON.sha256hex(canon), xml: ui.getXmlForPages(ui.pages) };
};
`;

async function loadXml(page, name, xml) {
  await page.evaluate(async ({ name, xml }) => {
    const p = H.wait(name, 'load', null, 60000);
    H.send(name, { action: 'load', xml, autosave: 0 });
    await p;
  }, { name, xml });
}

function goHashes(items) {
  const r = spawnSync(bin, ['xml'], { input: JSON.stringify(items), maxBuffer: 1 << 30 });
  if (r.status !== 0) throw new Error('go failed: ' + r.stderr);
  return JSON.parse(r.stdout.toString());
}

const server = await start(port);
const browser = await chromium.launch({ executablePath: CHROME, headless: true });
const ctx = await browser.newContext();
const corpus = [];
const R = { startedAt: new Date().toISOString(), tag, hand: [], random: {}, summary: {} };
try {
  const { page } = await freshPage(ctx, port);
  await page.addScriptTag({ path: path.join(here, 'canon.js') });
  await page.addScriptTag({ content: RANDOM_JS });
  await openEditor(page, 'A', BASE_XML, { diffSync: false });

  // hand-written corpus
  for (const [name, xml] of Object.entries(HAND)) {
    let live = null, loadError = null;
    try { await loadXml(page, 'A', xml); live = await page.evaluate(() => canonLive('A')); } catch (e) { loadError = String(e).slice(0, 200); }
    if (live) corpus.push({ name: 'hand:' + name, xml: live.xml, jsLive: live.hash });
    const [goRaw] = goHashes([{ name, xml }]);
    const [goExp] = live ? goHashes([{ name: name + ':exported', xml: live.xml }]) : [{}];
    R.hand.push({ name, loadError, jsLive: live && live.hash, goRaw: goRaw.hash || null, goRawError: goRaw.error || null, goExported: goExp.hash || null, goExportedError: goExp.error || null,
      liveEqRaw: !!live && live.hash === goRaw.hash, liveEqExported: !!live && live.hash === goExp.hash, rawEqExported: !!goRaw.hash && goRaw.hash === goExp.hash,
      canonLive: live && live.canon, canonGoRaw: goRaw.canon, canonGoExp: goExp.canon });
  }

  // random corpus
  let eq = 0, mism = [];
  const rawItems = [];
  for (let i = 0; i < RANDOM_DOCS; i++) {
    const seed = 1000 + i;
    await loadXml(page, 'A', BASE_XML);
    await page.evaluate(({ seed }) => randomEdits('A', seed, 25), { seed });
    const live = await page.evaluate(() => canonLive('A'));
    rawItems.push({ name: 'rnd' + seed, xml: live.xml, jsLive: live.hash, canon: live.canon });
    corpus.push({ name: 'rnd' + seed, xml: live.xml, jsLive: live.hash });
  }
  const goRes = goHashes(rawItems.map(({ name, xml }) => ({ name, xml })));
  rawItems.forEach((it, i) => {
    const g = goRes[i];
    if (g.hash === it.jsLive) eq++; else mism.push({ name: it.name, jsLive: it.jsLive, go: g.hash || null, error: g.error || null,
      firstDiff: g.canon ? firstDiff(it.canon, g.canon) : null });
  });
  R.random = { docs: RANDOM_DOCS, liveEqGoExported: eq, mismatches: mism.slice(0, 10), mismatchCount: mism.length, avgXmlBytes: Math.round(rawItems.reduce((s, x) => s + x.xml.length, 0) / rawItems.length) };

  // also: Go hash of the same document, re-parsed after a draw.io export->load->export round-trip is stable
  const rt = [];
  for (const name of ['userObject', 'edgePoints', 'groupsContainers', 'multiPage', 'unicode']) {
    await loadXml(page, 'A', HAND[name]);
    const a = await page.evaluate(() => canonLive('A'));
    await loadXml(page, 'A', a.xml);
    const b = await page.evaluate(() => canonLive('A'));
    rt.push({ name, stable: a.hash === b.hash });
  }
  R.roundTrip = rt;
  R.summary = {
    handTotal: R.hand.length,
    handAllThree: R.hand.filter((x) => x.liveEqRaw && x.liveEqExported).length,
    handExportedOnly: R.hand.filter((x) => !x.liveEqRaw && x.liveEqExported).map((x) => x.name),
    handFailing: R.hand.filter((x) => !x.liveEqExported).map((x) => ({ name: x.name, loadError: x.loadError, goRawError: x.goRawError, goExportedError: x.goExportedError })),
    randomEq: `${eq}/${RANDOM_DOCS}`,
  };
} finally {
  await browser.close();
  server.close();
}
function firstDiff(a, b) {
  const la = a.split('\n'), lb = b.split('\n');
  for (let i = 0; i < Math.max(la.length, lb.length); i++) if (la[i] !== lb[i]) return { line: i, js: la[i], go: lb[i] };
  return null;
}
fs.writeFileSync(path.join(outDir, `corpus-${tag}.json`), JSON.stringify(corpus));
R.finishedAt = new Date().toISOString();
fs.writeFileSync(path.join(outDir, `results-${tag}.json`), JSON.stringify(R, null, 2));
console.log(JSON.stringify({ summary: R.summary, random: R.random, roundTrip: R.roundTrip }, null, 1));
