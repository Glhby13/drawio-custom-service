// Per-diagram MCP tokens E2E against the lab (ADR-0016): only the owner creates them, a token opens exactly one diagram with at
// most its role, revocation / AI switch-off take effect on the next call, OAuth (Keycloak) bearer tokens are not accepted.
// Run: node e2e/phase10_mcp_token.mjs   (needs the platform with migration 003 and an mcp-server with MCP_OAUTH_ENABLED unset)
import { BASE, check, finish, launch, newPage, keycloakLogin } from './lib.mjs';

const browser = await launch();

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

let nextId = 1;
async function mcp(ctx, token, method, params) {
  const r = await ctx.request.post(BASE + '/mcp', { headers: Object.assign({ 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream' }, token ? { Authorization: 'Bearer ' + token } : {}), data: { jsonrpc: '2.0', id: nextId++, method, params }, failOnStatusCode: false });
  let j = null; try { j = await r.json(); } catch (e) { /* not json */ }
  return { status: r.status(), headers: r.headers(), json: j };
}
const tool = async (ctx, token, name, args) => {
  const r = await mcp(ctx, token, 'tools/call', { name, arguments: args });
  const text = r.json?.result?.content?.[0]?.text ?? r.json?.error?.message ?? '';
  return { ok: r.status === 200 && r.json?.result && !r.json.result.isError, text, status: r.status };
};
const waitLabels = (s, want, ms = 20000) => s.page.waitForFunction(w => {
  const m = window.__adapter.editorUi().editor.graph.getModel(), out = [];
  Object.values(m.cells).forEach(c => { if (c.vertex || c.edge) out.push((typeof c.value === 'string' ? c.value : '') || ''); });
  return JSON.stringify(out.sort()) === JSON.stringify(w.slice().sort());
}, want, { timeout: ms }).then(() => true).catch(() => false);

const owner = await login('e2e-team1'), other = await login('e2e-user2');
const ctx = owner.ctx;
const stamp = Date.now();
const id = (await api(owner, 'POST', '/documents', { title: 'MCP token ' + stamp })).json.document.id;
const id2 = (await api(owner, 'POST', '/documents', { title: 'MCP token other ' + stamp })).json.document.id;
await owner.page.goto(BASE + '/d/' + id, { waitUntil: 'domcontentloaded' });
await owner.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status')?.dataset.state === 'ready', null, { timeout: 90000 });

// ---- who may create tokens ---------------------------------------------------------------------------------------------------------
let r = await api(other, 'POST', '/documents/' + id + '/mcp-tokens', { name: 'x', role: 'editor', enableAI: true });
check('a user without access cannot create a token (diagram looks missing)', r.status === 404, r.status + ' ' + r.text.slice(0, 80));
await api(owner, 'PUT', '/documents/' + id + '/permissions', { subject: 'e2e-user2', role: 'editor' });
r = await api(other, 'POST', '/documents/' + id + '/mcp-tokens', { name: 'x', role: 'editor', enableAI: true });
check('an editor who is not the owner cannot create a token', r.status === 403, r.status + ' ' + r.text.slice(0, 80));
r = await api(other, 'GET', '/documents/' + id + '/mcp-tokens');
check('an editor cannot list the tokens either', r.status === 403, String(r.status));

// ---- creation: owner only, AI must be on, secret shown once, only a hint is kept ---------------------------------------------------------------
r = await api(owner, 'POST', '/documents/' + id + '/mcp-tokens', { name: 'Reporting Agent', role: 'editor', expiresInDays: 30 });
check('creating a token while AI is off asks for enableAI (409)', r.status === 409 && r.json?.error?.code === 'ai_disabled', r.status + ' ' + r.text.slice(0, 100));
check('bad input is refused (name, role, expiry)', (await api(owner, 'POST', '/documents/' + id + '/mcp-tokens', { name: '', role: 'editor', enableAI: true })).status === 400
  && (await api(owner, 'POST', '/documents/' + id + '/mcp-tokens', { name: 'a', role: 'owner', enableAI: true })).status === 400
  && (await api(owner, 'POST', '/documents/' + id + '/mcp-tokens', { name: 'a', role: 'editor', expiresInDays: 9999, enableAI: true })).status === 400);
r = await api(owner, 'POST', '/documents/' + id + '/mcp-tokens', { name: 'Reporting Agent', role: 'editor', expiresInDays: 30, enableAI: true });
const tok = r.json?.token;
check('owner creates a token (dsp_ prefix, returned once)', r.status === 201 && /^dsp_[A-Za-z0-9_-]{40,}$/.test(tok || ''), r.status + ' ' + r.text.slice(0, 60));
const tokId = r.json?.tokenInfo?.id;
r = await api(owner, 'GET', '/documents/' + id + '/mcp-tokens');
check('the list shows name, hint and role but never the token', r.status === 200 && r.json.tokens.length === 1 && r.json.tokens[0].hint === tok.slice(-4) && !r.text.includes(tok) && !('token' in r.json.tokens[0]), r.text.slice(0, 160));
r = await api(owner, 'GET', '/documents/' + id + '/permissions');
check('creating a token switched "allow AI" on', r.json?.aiPolicy?.allowed === true, JSON.stringify(r.json?.aiPolicy));

// ---- authentication ---------------------------------------------------------------------------------------------------------------
const noTok = await mcp(ctx, null, 'ping', {});
check('no token -> 401, no OAuth metadata advertised', noTok.status === 401 && !/resource_metadata/.test(noTok.headers['www-authenticate'] || ''), noTok.headers['www-authenticate']);
check('unknown dsp_ token -> 401', (await mcp(ctx, 'dsp_' + 'A'.repeat(43), 'ping', {})).status === 401);
check('an OAuth-style (JWT) bearer token is not accepted any more', (await mcp(ctx, 'eyJhbGciOiJSUzI1NiJ9.eyJhdWQiOiJkcmF3aW8tbWNwIn0.AAAA', 'ping', {})).status === 401);
check('OAuth protected-resource metadata is gone', (await ctx.request.get(BASE + '/.well-known/oauth-protected-resource', { failOnStatusCode: false })).status() !== 200);
const init = await mcp(ctx, tok, 'initialize', { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'e2e', version: '1' } });
check('initialize with the token', init.status === 200 && init.json?.result?.capabilities?.tools, JSON.stringify(init.json?.result?.serverInfo));
const names = ((await mcp(ctx, tok, 'tools/list', {})).json?.result?.tools || []).map(t => t.name);
check('catalogue: working tools, no create_diagram', names.includes('add_shape') && names.includes('get_diagram') && !names.includes('create_diagram'), names.join());

// ---- scope: exactly one diagram ----------------------------------------------------------------------------------------------------------
r = await tool(ctx, tok, 'list_diagrams', {});
check('list_diagrams shows only the token\'s diagram', r.ok && r.text.includes(id) && !r.text.includes(id2) && r.text.split('\n').filter(Boolean).length === 1, r.text.slice(0, 160));
r = await tool(ctx, tok, 'get_diagram', {});
check('"document" can be left out (defaults to the token\'s diagram)', r.ok && r.text.length > 20, r.text.slice(0, 80));
r = await tool(ctx, tok, 'get_diagram', { document: id2 });
check('another diagram of the same owner is refused', !r.ok && /only gives access/.test(r.text), r.text.slice(0, 100));
r = await tool(ctx, tok, 'create_diagram', { title: 'nope' });
check('a token cannot create diagrams', !r.ok && /cannot create/.test(r.text), r.text.slice(0, 100));
r = await tool(ctx, tok, 'add_shape', { label: 'From token', x: 80, y: 80 });
check('add_shape without naming the diagram works', r.ok, r.text.slice(0, 100));
check('the shape appears live in the owner\'s open editor', await waitLabels(owner, ['From token']));
r = await api(owner, 'GET', '/documents/' + id + '/history');
check('history attributes the change to the token\'s AI actor on behalf of the owner', /ai-reporting-agent/.test(r.text) && /e2e-team1/.test(r.text), r.text.slice(0, 200));
r = await api(owner, 'GET', '/documents/' + id + '/audit');
check('audit records the token creation', /mcp-token-create/.test(r.text), r.text.slice(0, 120));

// ---- role caps -------------------------------------------------------------------------------------------------------------------
r = await api(owner, 'POST', '/documents/' + id + '/mcp-tokens', { name: 'Read only', role: 'viewer' });
const viewerTok = r.json?.token;
check('create a viewer token', r.status === 201 && !!viewerTok, String(r.status));
r = await tool(ctx, viewerTok, 'get_diagram', {});
check('viewer token can read', r.ok, r.text.slice(0, 80));
r = await tool(ctx, viewerTok, 'add_shape', { label: 'Nope', x: 10, y: 10 });
check('viewer token cannot edit', !r.ok, r.text.slice(0, 100));

// ---- kill switches -----------------------------------------------------------------------------------------------------------------
r = await api(other, 'DELETE', '/documents/' + id + '/mcp-tokens/' + tokId);
check('only the owner can revoke (editor gets 403)', r.status === 403, String(r.status));
r = await api(owner, 'DELETE', '/documents/' + id + '/mcp-tokens/' + tokId);
check('owner revokes the token', r.status === 200, r.text.slice(0, 80));
check('a revoked token is refused on the very next call', (await mcp(ctx, tok, 'ping', {})).status === 401);
r = await api(owner, 'GET', '/documents/' + id + '/mcp-tokens');
check('the revoked token stays in the list as revoked', r.json?.tokens?.some(t => t.id === tokId && t.revokedAt), r.text.slice(0, 160));

const aiOff = (await api(owner, 'GET', '/documents/' + id + '/permissions')).json.aiPolicy;
await api(owner, 'PUT', '/documents/' + id + '/ai-policy', { allowed: false, maxOpsPerMin: aiOff.maxOpsPerMin, maxDelete: aiOff.maxDelete });
check('switching "allow AI" off stops the remaining token at once', (await mcp(ctx, viewerTok, 'ping', {})).status === 401);

// ---- UI: "MCP access" lives in the AI menu (not in Share) -------------------------------------------------------------------------------
const openAiMenu = async s => { await s.page.frameLocator('#editor').locator('[data-drawspace-menu="ai"]').click(); await s.page.waitForSelector('#ai-pop', { timeout: 10000 }); };
await owner.page.reload({ waitUntil: 'domcontentloaded' });
await owner.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status')?.dataset.state === 'ready', null, { timeout: 90000 });
await owner.page.click('#btn-share');
await owner.page.waitForSelector('#dlg[open] #btn-add-user', { timeout: 10000 });
check('the Share dialog no longer holds MCP tokens', (await owner.page.locator('#mcp-access').count()) === 0);
await owner.page.click('#dlg-actions .primary');
await openAiMenu(owner);
check('owner sees "MCP access…" in the AI menu', await owner.page.locator('#ai-mcp').isVisible());
await owner.page.click('#ai-mcp');
await owner.page.waitForSelector('#dlg[open] #mcp-access', { timeout: 10000 });
check('the dialog is titled MCP access and names no vendor', (await owner.page.textContent('#dlg-title')) === 'MCP access' && !/claude|anthropic|openai|gpt/i.test(await owner.page.textContent('#dlg')));
await owner.page.fill('#mcp-name', 'UI token');
await owner.page.click('#mcp-create');
await owner.page.waitForSelector('#mcp-token', { timeout: 15000 }).catch(() => {});
const shown = await owner.page.inputValue('#mcp-token').catch(() => '');
check('the UI shows the new token once and lists it', /^dsp_/.test(shown) && await owner.page.locator('.mcp-row[data-token]').count() >= 1, shown.slice(0, 8));
check('the UI offers "Copy token" and "Copy MCP config" (no vendor-specific command)', (await owner.page.locator('.mcp-actions button').allTextContents()).join('|') === 'Copy token|Copy MCP config' && !/claude mcp add/i.test(await owner.page.textContent('#mcp-created')));
r = await mcp(ctx, shown, 'ping', {});
check('the token created in the UI works', r.status === 200, String(r.status));
await owner.page.click('#dlg-actions .primary');
await openAiMenu(owner);
check('the AI menu shows how many tokens are active', /active/.test(await owner.page.textContent('#ai-mcp')), await owner.page.textContent('#ai-mcp'));
await owner.page.keyboard.press('Escape');

await other.page.goto(BASE + '/d/' + id, { waitUntil: 'domcontentloaded' });
await other.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status')?.dataset.state === 'ready', null, { timeout: 90000 });
await other.page.frameLocator('#editor').locator('[data-drawspace-menu="ai"]').click();
await other.page.waitForTimeout(1500); // an editor is not allowed to open the AI settings at all
check('a non-owner does not get "MCP access…" in the AI menu', (await other.page.locator('#ai-mcp').count()) === 0 && (await other.page.locator('#mcp-access').count()) === 0);

await browser.close();
finish();
