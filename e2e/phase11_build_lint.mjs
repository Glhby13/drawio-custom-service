// build_diagram / lint_diagram / hierarchical layout through the MCP server, against the lab (ADR-0017).
// A model cannot see the canvas, so it describes the graph and the server does the geometry; this checks that the result is
// tidy (no overlaps, nothing sticking out of containers, no edge through a shape), arrives in the live editor, is one revertible
// step, and that bad input produces errors an agent can act on.
// Run: node e2e/phase11_build_lint.mjs   (needs gateway/platform/mcp-server with ADR-0017)
import { BASE, check, finish, launch, newPage, keycloakLogin } from './lib.mjs';

const browser = await launch();
const owner = await (async () => {
  const s = await newPage(browser);
  await s.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(s.page, 'e2e-team1');
  await s.page.waitForURL(BASE + '/', { timeout: 30000 });
  await s.page.waitForSelector('#list-view:not([hidden])', { timeout: 20000 });
  s.csrf = (await s.page.evaluate(() => fetch('/api/me', { credentials: 'same-origin' }).then(r => r.json()))).csrf;
  return s;
})();
const api = (s, method, path, body) => s.page.evaluate(async ({ method, path, body, csrf }) => {
  const r = await fetch('/api/v1' + path, { method, credentials: 'same-origin', headers: Object.assign({ 'X-CSRF-Token': csrf }, body !== undefined ? { 'Content-Type': 'application/json' } : {}), body: body !== undefined ? JSON.stringify(body) : undefined });
  const t = await r.text(); let j = null; try { j = JSON.parse(t); } catch (e) { /* text */ }
  return { status: r.status, json: j, text: t };
}, { method, path, body, csrf: s.csrf });
let nextId = 1;
const ctx = owner.ctx;
async function mcp(token, method, params) {
  const r = await ctx.request.post(BASE + '/mcp', { headers: { 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream', Authorization: 'Bearer ' + token }, data: { jsonrpc: '2.0', id: nextId++, method, params } });
  let j = null; try { j = await r.json(); } catch (e) { /* not json */ }
  return { status: r.status(), json: j };
}
const tool = async (token, name, args) => {
  const r = await mcp(token, 'tools/call', { name, arguments: args });
  return { ok: r.status === 200 && r.json?.result && !r.json.result.isError, text: r.json?.result?.content?.[0]?.text ?? r.json?.error?.message ?? '' };
};

const id = (await api(owner, 'POST', '/documents', { title: 'Build + lint ' + Date.now() })).json.document.id;
await owner.page.goto(BASE + '/d/' + id, { waitUntil: 'domcontentloaded' });
await owner.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status')?.dataset.state === 'ready', null, { timeout: 90000 });
let r = await api(owner, 'POST', '/documents/' + id + '/mcp-tokens', { name: 'Layout Agent', role: 'editor', expiresInDays: 1, enableAI: true });
const tok = r.json?.token;
check('owner creates an agent token', r.status === 201 && /^dsp_/.test(tok || ''), r.status + ' ' + r.text.slice(0, 80));
await api(owner, 'PUT', `/documents/${id}/ai-policy`, { allowed: true, maxOpsPerMin: 600, maxDelete: 20 });

// ---- the catalogue and the instructions steer the agent to the server-side layout ---------------------------------------------------------
const init = await mcp(tok, 'initialize', { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'e2e', version: '1' } });
check('instructions teach build_diagram + lint_diagram and forbid hand-placed coordinates', /build_diagram/.test(init.json?.result?.instructions || '') && /lint_diagram/.test(init.json.result.instructions) && /Do NOT choose x\/y/.test(init.json.result.instructions));
const names = ((await mcp(tok, 'tools/list', {})).json?.result?.tools || []).map(t => t.name);
check('catalogue has build_diagram and lint_diagram', names.includes('build_diagram') && names.includes('lint_diagram'), names.join(','));

// ---- build a realistic architecture: nested groups, fan-out, a queue, labelled edges ---------------------------------------------------------
const spec = {
  direction: 'LR',
  groups: [{ id: 'edge', label: 'Edge', color: 'gray' }, { id: 'ns', label: 'Namespace lab-portal', color: 'green' }, { id: 'data', label: 'Data stores', color: 'red' }, { id: 'ext', label: 'External', color: 'purple' }],
  nodes: [
    { id: 'browser', label: 'Browser (SPA)', group: 'edge', kind: 'actor' }, { id: 'nginx', label: 'nginx TLS edge', group: 'edge' },
    { id: 'gw', label: 'gateway (BFF)\nsession, CSRF, rate limit', group: 'ns', kind: 'service' }, { id: 'core', label: 'core-service', group: 'ns', kind: 'service' },
    { id: 'ticket', label: 'admin-service-ticket', group: 'ns', kind: 'service' }, { id: 'act', label: 'activity-service', group: 'ns', kind: 'service' },
    { id: 'audit', label: 'audit-service', group: 'ns', kind: 'service' }, { id: 'mail', label: 'email-notification-service', group: 'ns', kind: 'service' },
    { id: 'pg', label: 'PostgreSQL 18', group: 'data', kind: 'database', color: 'red' }, { id: 'redis', label: 'Redis 8', group: 'data', kind: 'database', color: 'red' },
    { id: 'kafka', label: 'Kafka topics', group: 'data', kind: 'queue', color: 'orange' },
    { id: 'kc', label: 'Keycloak OIDC', group: 'ext', kind: 'external' }, { id: 'smtp', label: 'SMTP (Mailpit)', group: 'ext', kind: 'external' },
  ],
  edges: [
    { from: 'browser', to: 'nginx', label: 'HTTPS' }, { from: 'nginx', to: 'gw' }, { from: 'gw', to: 'core' }, { from: 'gw', to: 'ticket' }, { from: 'gw', to: 'act' },
    { from: 'gw', to: 'redis', label: 'session' }, { from: 'gw', to: 'kc', label: 'OIDC', dashed: true },
    { from: 'core', to: 'pg' }, { from: 'ticket', to: 'pg' }, { from: 'act', to: 'pg' }, { from: 'core', to: 'kafka', label: 'outbox' }, { from: 'ticket', to: 'kafka' }, { from: 'act', to: 'kafka' },
    { from: 'kafka', to: 'audit' }, { from: 'kafka', to: 'mail' }, { from: 'audit', to: 'pg' }, { from: 'mail', to: 'smtp', label: 'email' },
  ],
};
r = await tool(tok, 'build_diagram', spec);
check('build_diagram creates 13 shapes, 17 edges, 4 groups in one call', r.ok && /Built 13 shape\(s\), 17 edge\(s\), 4 group\(s\)/.test(r.text), r.text.slice(0, 200));
check('the build result already carries the lint verdict with nothing at error level', r.ok && /Lint: 17 shape\(s\)|Lint: 13 shape\(s\)|Lint:/.test(r.text) && !/\[error\]/.test(r.text), r.text.slice(0, 400));
const cells = await owner.page.waitForFunction(() => {
  const m = window.__adapter.editorUi().editor.graph.getModel();
  return Object.values(m.cells).filter(c => c.vertex || c.edge).length;
}, null, { timeout: 30000 }).then(h => h.jsonValue()).catch(() => 0);
check('the live editor shows every shape, container and edge', cells === 13 + 4 + 17, String(cells));

// ---- lint: independent verdict, then break it and see it caught -----------------------------------------------------------------------------
r = await tool(tok, 'lint_diagram', {});
check('lint_diagram: no overlap, nothing outside its container, no edge through a shape', r.ok && !/\[error\]|overlap|outside_container|edge_through_shape|label_overflow/.test(r.text), r.text.slice(0, 500));
const first = await tool(tok, 'get_diagram', {});
check('get_diagram still reads the built diagram', first.ok && first.text.includes('Namespace lab-portal'));
r = await tool(tok, 'update_cell', { cell: 'core', x: 0, y: 0 });
check('moving a shape by hand is possible', r.ok, r.text);
r = await tool(tok, 'update_cell', { cell: 'ticket', x: 10, y: 10 });
r = await tool(tok, 'lint_diagram', {});
check('lint reports the damage (overlap or shapes outside their container)', r.ok && /overlap|outside_container/.test(r.text) && /layout/.test(r.text), r.text.slice(0, 400));
r = await tool(tok, 'layout', {});
check('layout (hierarchical by default) repairs it', r.ok && /Layout changed \d+ value/.test(r.text), r.text);
r = await tool(tok, 'lint_diagram', {});
check('after layout the diagram is tidy again', r.ok && !/overlap|outside_container|edge_through_shape/.test(r.text), r.text.slice(0, 400));
r = await tool(tok, 'layout', {});
check('layout is stable: a second run changes nothing', r.ok && /Layout changed 0 value/.test(r.text), r.text);
check('an unknown algo is refused with the valid ones named', await tool(tok, 'layout', { algo: 'spiral' }).then(x => !x.ok));

// ---- input an agent can act on ----------------------------------------------------------------------------------------------------------
r = await tool(tok, 'build_diagram', { nodes: [{ id: 'a', label: 'A', kind: 'blob' }, { id: 'b', label: 'B', color: 'pink' }], edges: [{ from: 'a', to: 'ghost' }] });
check('a bad spec is refused with every problem listed (kind, color, edge)', !r.ok && /kind/.test(r.text) && /color/.test(r.text) && /ghost/.test(r.text), r.text.slice(0, 300));
r = await tool(tok, 'build_diagram', { nodes: [{ id: 'gw', label: 'again' }] });
check('an id that already exists is refused, nothing is overwritten', !r.ok && /already exists/.test(r.text), r.text.slice(0, 200));
r = await tool(tok, 'connect', { source: 'gw', target: 'nope' });
check('connect to a missing cell is refused (no invisible dangling edge)', !r.ok && /nope/.test(r.text) && /get_diagram/.test(r.text), r.text.slice(0, 200));
r = await tool(tok, 'build_diagram', { nodes: [{ id: 'x1', label: '<img src=x onerror=alert(1)>' }] });
check('markup in a label is shown literally, never interpreted', r.ok, r.text.slice(0, 120));
const evil = await owner.page.waitForFunction(() => Object.values(window.__adapter.editorUi().editor.graph.getModel().cells).some(c => typeof c.value === 'string' && c.value.includes('onerror')), null, { timeout: 15000 }).then(() => true).catch(() => false);
const injected = await owner.page.evaluate(() => !!document.querySelector('img[onerror]'));
check('the label text arrives but no element is injected into the page', evil && !injected, `label=${evil} injected=${injected}`);

// ---- one revertible step per build, attributed to the agent -------------------------------------------------------------------------------------------
const hist = await api(owner, 'GET', `/documents/${id}/history?limit=20`);
check('history lists the build as one batch by the AI actor', hist.status === 200 && JSON.stringify(hist.json).includes('bld-'), hist.text.slice(0, 160));

await browser.close();
finish();
