// Phases 6-7 E2E against the lab: MCP server (OAuth bearer from Keycloak, Streamable HTTP) and the AI collaborator
// (delegated identity, owner policy, explicit share, quotas, attribution, revert, realtime visibility to people).
// Run: node e2e/phase6_mcp.mjs
import { BASE, check, finish, launch, newPage, keycloakLogin, password } from './lib.mjs';

const browser = await launch();
const sleep = ms => new Promise(r => setTimeout(r, ms));
const KC = 'https://auth.lab.local/realms/lab/protocol/openid-connect/token';

async function login(user) {
  const s = await newPage(browser);
  await s.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(s.page, user);
  await s.page.waitForURL(BASE + '/', { timeout: 30000 });
  await s.page.waitForSelector('#list-view:not([hidden])', { timeout: 20000 });
  s.csrf = (await s.page.evaluate(() => fetch('/api/me', { credentials: 'same-origin' }).then(r => r.json()))).csrf;
  return s;
}
const api = (s, method, path, body) => s.page.evaluate(async ({ method, path, body, csrf }) => {
  const r = await fetch('/api/v1' + path, { method, credentials: 'same-origin', headers: Object.assign({ 'X-CSRF-Token': csrf }, body !== undefined ? { 'Content-Type': 'application/json' } : {}), body: body !== undefined ? JSON.stringify(body) : undefined });
  const t = await r.text(); let j = null; try { j = JSON.parse(t); } catch (e) { /* text */ }
  return { status: r.status, json: j, text: t };
}, { method, path, body, csrf: s.csrf });

// an agent obtains an access token for a real user (lab: direct grant; production agents use auth code + PKCE)
async function agentToken(ctx, user) {
  const r = await ctx.request.post(KC, { form: { grant_type: 'password', client_id: 'drawio-mcp-claude', username: user, password: password(user), scope: 'openid profile' }, failOnStatusCode: false });
  if (r.status() !== 200) throw new Error('token for ' + user + ': ' + r.status() + ' ' + (await r.text()).slice(0, 200));
  return (await r.json()).access_token;
}
let nextId = 1;
async function mcp(ctx, token, method, params, hdr = {}) {
  const r = await ctx.request.post(BASE + '/mcp', { headers: Object.assign({ 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream' }, token ? { Authorization: 'Bearer ' + token } : {}, hdr), data: { jsonrpc: '2.0', id: nextId++, method, params }, failOnStatusCode: false });
  let j = null; try { j = await r.json(); } catch (e) { /* not json */ }
  return { status: r.status(), headers: r.headers(), json: j };
}
const tool = async (ctx, token, name, args, hdr) => {
  const r = await mcp(ctx, token, 'tools/call', { name, arguments: args }, hdr);
  const text = r.json?.result?.content?.[0]?.text ?? r.json?.error?.message ?? '';
  return { ok: r.status === 200 && r.json?.result && !r.json.result.isError, text, status: r.status, raw: r.json };
};
const waitLabels = (s, want, ms = 20000) => s.page.waitForFunction(w => {
  const m = window.__adapter.editorUi().editor.graph.getModel(), out = [];
  Object.values(m.cells).forEach(c => { if (c.vertex || c.edge) out.push((typeof c.value === 'string' ? c.value : '') || ''); });
  return JSON.stringify(out.sort()) === JSON.stringify(w.slice().sort());
}, want, { timeout: ms }).then(() => true).catch(() => false);

const alice = await login('e2e-team1'), carol = await login('e2e-user2');
const ctx = alice.ctx; // any APIRequestContext (ignoreHTTPSErrors) works for the agent
const id = (await api(alice, 'POST', '/documents', { title: 'AI collaboration ' + Date.now() })).json.document.id;
await alice.page.goto(BASE + '/d/' + id, { waitUntil: 'domcontentloaded' });
await alice.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status')?.dataset.state === 'ready', null, { timeout: 90000 });

// ---- discovery + authentication ------------------------------------------------------------------------------------
const meta = await (await ctx.request.get(BASE + '/.well-known/oauth-protected-resource')).json();
check('protected resource metadata names the resource and Keycloak as authorization server', meta.resource === BASE + '/mcp' && meta.authorization_servers?.[0] === 'https://auth.lab.local/realms/lab', JSON.stringify(meta));
const noTok = await mcp(ctx, null, 'ping', {});
check('no token -> 401 with WWW-Authenticate pointing at the metadata', noTok.status === 401 && /resource_metadata=/.test(noTok.headers['www-authenticate'] || ''), noTok.headers['www-authenticate']);
check('forged bearer token -> 401', (await mcp(ctx, 'eyJhbGciOiJSUzI1NiJ9.e30.AAAA', 'ping', {})).status === 401);
const tokA = await agentToken(ctx, 'e2e-team1');
const init = await mcp(ctx, tokA, 'initialize', { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'e2e', version: '1' } });
check('initialize: protocol version + tools capability', init.json?.result?.protocolVersion && init.json.result.capabilities.tools, JSON.stringify(init.json?.result?.serverInfo));
const tl = await mcp(ctx, tokA, 'tools/list', {});
const names = (tl.json?.result?.tools || []).map(t => t.name);
check('tool catalogue', ['list_diagrams', 'get_diagram', 'create_diagram', 'add_shape', 'connect', 'update_cell', 'delete_cells', 'apply_operations', 'layout', 'export', 'get_history', 'list_versions', 'save_version', 'restore_version', 'comment'].every(n => names.includes(n)), names.join());
check('a user token for a client without the audience is not accepted by this server', await (async () => {
  const r = await ctx.request.post(KC, { form: { grant_type: 'password', client_id: 'admin-cli', username: 'e2e-team1', password: password('e2e-team1') }, failOnStatusCode: false });
  if (r.status() !== 200) return true; // client not usable for the password grant: nothing to attack with
  return (await mcp(ctx, (await r.json()).access_token, 'ping', {})).status === 401;
})());

// ---- the AI sees nothing until the owner allows AI AND shares the diagram with it ---------------------------------------------------
let r = await tool(ctx, tokA, 'list_diagrams', {});
check('before any sharing the agent sees no diagram', r.ok && !r.text.includes(id), r.text.slice(0, 100));
r = await tool(ctx, tokA, 'get_diagram', { document: id });
check('reading a diagram that does not allow AI is refused (404-style)', !r.ok && /not found/.test(r.text), r.text.slice(0, 120));
check('owner enables AI collaborators on the diagram', (await api(alice, 'PUT', `/documents/${id}/ai-policy`, { allowed: true, maxOpsPerMin: 60, maxDelete: 2 })).status === 200);
r = await tool(ctx, tokA, 'get_diagram', { document: id });
check('AI allowed but not shared with the AI actor -> still nothing', !r.ok, r.text.slice(0, 120));
const users = await api(alice, 'GET', '/users?q=ai-claude');
check('the AI actor appears in the share picker, marked as AI', users.json.users.some(u => u.username === 'ai-claude' && u.kind === 'ai'), JSON.stringify(users.json.users));
check('owner shares the diagram with ai-claude (editor)', (await api(alice, 'PUT', `/documents/${id}/permissions`, { subject: 'ai-claude', role: 'editor' })).status === 200);

// ---- the AI edits; people see it live ---------------------------------------------------------------------------------------
r = await tool(ctx, tokA, 'add_shape', { document: id, label: 'Client', x: 40, y: 40, style: 'rounded=1;fillColor=#dae8fc;' }, { 'X-Agent-Model': 'claude-test' });
check('AI adds a shape', r.ok, r.text);
const shapeA = /shape (\S+)/.exec(r.text)?.[1];
r = await tool(ctx, tokA, 'add_shape', { document: id, label: 'Server', x: 300, y: 40 });
const shapeB = /shape (\S+)/.exec(r.text)?.[1];
r = await tool(ctx, tokA, 'connect', { document: id, source: shapeA, target: shapeB, label: 'calls' });
check('AI connects the shapes', r.ok, r.text);
check('the AI actor shows up in the presence bar (marked AI) while it works', await alice.page.waitForFunction(() => [...document.querySelectorAll('#presence .pav.ai')].some(x => x.dataset.user === 'ai-claude'), null, { timeout: 15000 }).then(() => true).catch(() => false), await alice.page.evaluate(() => [...document.querySelectorAll('#presence .pav')].map(x => x.dataset.user + ':' + x.title).join(' | ')));
check('the owner, watching in the browser, sees the AI\'s shapes appear in realtime', await waitLabels(alice, ['Client', 'Server', 'calls']));
r = await tool(ctx, tokA, 'layout', { document: id, algo: 'layered' });
check('AI arranges the diagram (layout tool)', r.ok, r.text);
r = await tool(ctx, tokA, 'update_cell', { document: id, cell: shapeB, label: 'API server', style: 'fillColor=#f8cecc;' });
check('AI updates a cell', r.ok, r.text);
check('update replicates to the browser', await waitLabels(alice, ['API server', 'Client', 'calls']));
r = await tool(ctx, tokA, 'get_diagram', { document: id });
check('get_diagram returns a structured summary with ids and labels', r.ok && r.text.includes('API server') && r.text.includes(shapeA), r.text.slice(0, 80));
r = await tool(ctx, tokA, 'export', { document: id, format: 'svg' });
check('AI exports SVG', r.ok && r.text.includes('<svg'));
r = await tool(ctx, tokA, 'comment', { document: id, body: 'Review: the server box may need a load balancer' });
check('AI comments', r.ok, r.text);
const cm = await api(alice, 'GET', `/documents/${id}/comments`);
check('the comment is attributed to the AI actor', cm.json.comments.some(c => c.author === 'ai-claude' && c.authorType === 'ai'));
r = await tool(ctx, tokA, 'save_version', { document: id, name: 'ai checkpoint' });
check('AI saves a named version', r.ok, r.text.slice(0, 80));
const res = await mcp(ctx, tokA, 'resources/list', {});
check('resources/list exposes diagram:// resources', res.json?.result?.resources?.some(x => x.uri === 'diagram://' + id));

// ---- attribution + revert -----------------------------------------------------------------------------------------------------
const hist = (await api(alice, 'GET', `/documents/${id}/history?limit=50`)).json.history;
const ai = hist.filter(h => h.actorType === 'ai');
check('history attributes every AI change: AI actor, on behalf of the user, agent, model', ai.length >= 4 && ai.every(h => h.actor === 'ai-claude' && h.onBehalfOf === 'e2e-team1' && h.agent === 'drawio-mcp-claude') && ai.some(h => h.model === 'claude-test'), `${ai.length} AI batches, e.g. ${JSON.stringify(ai[0])}`);
const aud = (await api(alice, 'GET', `/documents/${id}/audit`)).json.events;
check('audit trail records AI operations with the delegating user', aud.some(e => e.actorType === 'ai' && e.onBehalfOf === 'e2e-team1' && e.action === 'ops'));
const upd = hist.find(h => h.actorType === 'ai' && h.types.includes('setProp'));
const rv = await api(alice, 'POST', `/documents/${id}/revert`, { batch: upd.batch });
check('the owner reverts the AI\'s update as with any other change', rv.status === 200, JSON.stringify(rv.json));
check('revert is visible to the owner in the browser', await waitLabels(alice, ['Server', 'Client', 'calls']));

// ---- limits and delegation ----------------------------------------------------------------------------------------------------------
r = await tool(ctx, tokA, 'add_shape', { document: id, label: 'Third', x: 40, y: 200 });
const shapeC = /shape (\S+)/.exec(r.text)?.[1];
r = await tool(ctx, tokA, 'delete_cells', { document: id, cells: [shapeA, shapeB, shapeC] });
check('deleting more cells in one request than the owner allows (maxDelete=2) needs confirmation', !r.ok && /confirmation required/.test(r.text), r.text.slice(0, 160));
check('nothing was deleted by the refused request', await waitLabels(alice, ['Server', 'Client', 'calls', 'Third']));
r = await tool(ctx, tokA, 'delete_cells', { document: id, cells: [shapeC] });
check('a deletion within the limit succeeds', r.ok, r.text);
check('owner lowers the AI rate limit to 3 ops/min', (await api(alice, 'PUT', `/documents/${id}/ai-policy`, { allowed: true, maxOpsPerMin: 3, maxDelete: 2 })).status === 200);
r = await tool(ctx, tokA, 'apply_operations', { document: id, operations: Array.from({ length: 5 }, (_, i) => ({ t: 'setProp', page: 'p1', cell: shapeA, key: 'v', val: 'x' + i })) });
check('exceeding the AI quota is refused (rate limited)', !r.ok && /rate limited/.test(r.text), r.text.slice(0, 120));

// a viewer's agent: the AI is capped by the delegating user's own role
check('owner shares with carol as viewer only', (await api(alice, 'PUT', `/documents/${id}/permissions`, { subject: 'e2e-user2', role: 'viewer' })).status === 200);
await api(alice, 'PUT', `/documents/${id}/ai-policy`, { allowed: true, maxOpsPerMin: 60, maxDelete: 2 });
const tokC = await agentToken(ctx, 'e2e-user2');
r = await tool(ctx, tokC, 'get_diagram', { document: id });
check('an agent acting for a viewer may read (role = min(AI share, user role))', r.ok, r.text.slice(0, 80));
r = await tool(ctx, tokC, 'add_shape', { document: id, label: 'should not exist' });
check('... but cannot edit: the delegating user is only a viewer', !r.ok && /forbidden/.test(r.text), r.text.slice(0, 120));
const lc = await tool(ctx, tokC, 'list_diagrams', {});
check('an agent acting for a user the diagram is not shared with sees nothing', await (async () => {
  const stranger = await agentToken(ctx, 'lab-test2');
  const x = await tool(ctx, stranger, 'get_diagram', { document: id });
  return !x.ok && /not found/.test(x.text);
})(), lc.text.slice(0, 60));

// ---- the owner can switch the AI off at any time -----------------------------------------------------------------------------------------
await api(alice, 'PUT', `/documents/${id}/ai-policy`, { allowed: false, maxOpsPerMin: 60, maxDelete: 2 });
r = await tool(ctx, tokA, 'add_shape', { document: id, label: 'after switch-off' });
check('with AI disabled by the owner, the agent is locked out immediately', !r.ok && /not found|forbidden/.test(r.text), r.text.slice(0, 100));
check('no AI-written shape remained from the refused calls', await waitLabels(alice, ['Server', 'Client', 'calls']));

// ---- create_diagram: AI-created diagram is owned by the delegating user ---------------------------------------------------------------------
r = await tool(ctx, tokA, 'create_diagram', { title: 'Created by AI' });
const newId = /diagram (\S+)/.exec(r.text)?.[1];
check('AI creates a diagram', r.ok && newId, r.text);
const mine = await api(alice, 'GET', '/documents');
check('the new diagram is owned by the delegating user and visible to them', mine.json.documents.some(d => d.id === newId && d.owner === 'e2e-team1' && d.role === 'owner' && d.aiPolicy.allowed));
check('cleanup', (await api(alice, 'DELETE', `/documents/${newId}`)).status === 200 && (await api(alice, 'DELETE', `/documents/${id}`)).status === 200);

await browser.close();
finish();
