// Phase 2 (gateway) E2E in real Chrome against the lab: OIDC login, editor through the gateway, coarse role check,
// no bypass, logout, upstream outage. Run: node e2e/phase2_gateway.mjs
import { BASE, check, finish, launch, newPage, keycloakLogin, kubectl } from './lib.mjs';

const browser = await launch();
const origins = log => [...new Set(log.requests.filter(u => /^https?:/.test(u)).map(u => new URL(u).origin))];

// 1. unauthenticated -> login
{
  const { ctx, page } = await newPage(browser);
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await page.waitForURL(/auth\.lab\.local/, { timeout: 30000 });
  check('unauthenticated navigation is sent to Keycloak login', /auth\.lab\.local\/realms\/lab/.test(page.url()), new URL(page.url()).origin);
  const api = await ctx.request.get(BASE + '/editor/', { failOnStatusCode: false, maxRedirects: 0 });
  check('unauthenticated /editor/ is not served', api.status() === 302 || api.status() === 401, 'status ' + api.status());
  const me = await ctx.request.get(BASE + '/api/me', { failOnStatusCode: false, maxRedirects: 0 });
  check('unauthenticated /api/me = 401', me.status() === 401, 'status ' + me.status());
  await ctx.close();
}

// 2. authorised user: login, editor, session cookie, edit
let sessionCookieValue;
{
  const { ctx, page, log } = await newPage(browser);
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(page, 'e2e-team1');
  await page.waitForURL(BASE + '/', { timeout: 30000 });
  await page.waitForSelector('#list-view:not([hidden])', { timeout: 20000 });
  check('login lands on the document list', true);
  const csrf0 = (await page.evaluate(() => fetch('/api/me', { credentials: 'same-origin' }).then(r => r.json()))).csrf;
  const docId = await page.evaluate(async c => (await (await fetch('/api/v1/documents', { method: 'POST', headers: { 'X-CSRF-Token': c, 'Content-Type': 'application/json' }, body: '{"title":"phase2 smoke"}' })).json()).document.id, csrf0);
  await page.goto(BASE + '/d/' + docId, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => document.getElementById('status')?.dataset.state === 'ready', null, { timeout: 60000 });
  check('editor handshake completes (adapter loaded under CSP)', true, 'status data-state=ready');
  const frame = page.frames().find(f => f.url().includes('/editor/'));
  check('editor iframe is served from /editor/ through the gateway', !!frame && new URL(frame.url()).origin === BASE, frame && new URL(frame.url()).pathname);
  const cookies = (await ctx.cookies()).filter(c => c.name.includes('drawio'));
  const sid = cookies.find(c => c.name === '__Host-drawio_sid');
  sessionCookieValue = sid?.value;
  check('session cookie flags (__Host-, HttpOnly, Secure, SameSite=Lax, Path=/)', sid && sid.httpOnly && sid.secure && sid.sameSite === 'Lax' && sid.path === '/', sid && `httpOnly=${sid.httpOnly} secure=${sid.secure} sameSite=${sid.sameSite}`);
  check('cookie is invisible to page scripts', !(await page.evaluate(() => document.cookie)).includes('drawio'), 'document.cookie has no drawio cookie');
  check('login-binding cookie is cleared after callback', !cookies.some(c => c.name === '__Host-drawio_login'), cookies.map(c => c.name).join(','));
  const me = await page.evaluate(() => fetch('/api/me', { credentials: 'same-origin' }).then(r => r.json()));
  check('/api/me returns the Keycloak identity and roles', me.username === 'e2e-team1' && me.roles.includes('user') && me.csrf, `${me.username} roles=${me.roles}`);
  // real editing inside the proxied official draw.io
  const edit = await frame.evaluate(() => new Promise(res => {
    Draw.loadPlugin(ui => {
      const g = ui.editor.graph; g.getModel().beginUpdate();
      try { g.insertVertex(g.getDefaultParent(), null, 'Phase2', 40, 40, 120, 60); } finally { g.getModel().endUpdate(); }
      res(g.getModel().getChildCount(g.getDefaultParent()));
    });
  }));
  check('shape can be created in the editor served via the gateway', edit === 1, 'cells=' + edit);
  const viol = (await page.evaluate(() => window.__csp || [])).concat(await frame.evaluate(() => window.__csp || []));
  check('no CSP violations in host page or editor frame', viol.length === 0, JSON.stringify(viol.slice(0, 3)));
  const ext = origins(log).filter(o => o !== BASE && o !== 'https://auth.lab.local');
  check('no network request leaves drawio.lab.local / auth.lab.local', ext.length === 0, JSON.stringify(origins(log)));
  check('no console errors', log.consoleErrors.length === 0, log.consoleErrors.slice(0, 2).join(' | '));
  const hdr = (await ctx.request.get(BASE + '/editor/', { failOnStatusCode: false })).headers();
  check('proxied editor response is framed same-origin only and leaks no server banner', hdr['x-frame-options'] === 'SAMEORIGIN' && /frame-ancestors 'self'/.test(hdr['content-security-policy'] || '') && !hdr['server']?.toLowerCase().includes('coyote') && !hdr['set-cookie'], hdr['server'] + ' | ' + hdr['x-frame-options']);
  // 3. tampering
  await ctx.clearCookies();
  const forged = await ctx.request.get(BASE + '/api/me', { failOnStatusCode: false, headers: { Cookie: '__Host-drawio_sid=forgedforgedforged' } });
  check('forged session cookie is rejected', forged.status() === 401, 'status ' + forged.status());
  // logout in a fresh context that re-logs in
  await ctx.close();
}

// 4. user without client role is refused, no session issued
{
  const { ctx, page } = await newPage(browser);
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(page, 'e2e-team2');
  await page.waitForURL(/drawio\.lab\.local\/auth\/callback/, { timeout: 30000 }).catch(() => {});
  await page.waitForLoadState('domcontentloaded');
  const body = await page.textContent('body');
  const sid = (await ctx.cookies()).find(c => c.name === '__Host-drawio_sid');
  check('authenticated user without the drawio-lab role is denied (403) and gets no session', /access denied/i.test(body) && !sid, (body || '').trim().slice(0, 80));
  const direct = await ctx.request.get(BASE + '/editor/', { failOnStatusCode: false, maxRedirects: 0 });
  check('... and still cannot reach the editor', direct.status() !== 200, 'status ' + direct.status());
  await ctx.close();
}

// 5. logout is end-to-end (gateway session + Keycloak SSO)
{
  const { ctx, page } = await newPage(browser);
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(page, 'e2e-user2');
  await page.waitForSelector('#list-view:not([hidden])', { timeout: 60000 });
  const before = (await ctx.cookies()).find(c => c.name === '__Host-drawio_sid')?.value;
  await page.click('#logout');
  await page.waitForURL(/drawio\.lab\.local\/logged-out/, { timeout: 30000 });
  check('logout returns through Keycloak end_session to /logged-out', true, page.url());
  const stale = await ctx.request.get(BASE + '/api/me', { failOnStatusCode: false, headers: { Cookie: '__Host-drawio_sid=' + before } });
  check('old session cookie is dead after logout (server-side deletion)', stale.status() === 401, 'status ' + stale.status());
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await page.waitForURL(/auth\.lab\.local/, { timeout: 30000 });
  const loginForm = await page.locator('#username').count();
  check('Keycloak SSO session ended too: login form is shown, no silent re-login', loginForm === 1, 'login form present');
  await ctx.close();
}

// 6. upstream outage: draw.io scaled to zero -> gateway answers 502, never an empty/200 page
{
  kubectl('-n lab-dev scale deploy/lab-drawio-dev-lab --replicas=0 >/dev/null && sleep 6');
  const { ctx, page } = await newPage(browser);
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(page, 'e2e-team1');
  await page.waitForURL(BASE + '/', { timeout: 30000 });
  const r = await ctx.request.get(BASE + '/editor/', { failOnStatusCode: false });
  check('draw.io unavailable -> gateway returns 502 (fail visible)', r.status() === 502, 'status ' + r.status());
  const ready = await ctx.request.get(BASE + '/readyz', { failOnStatusCode: false });
  check('gateway itself stays ready (it depends on Redis + IdP, not on draw.io)', ready.status() === 200, 'readyz ' + ready.status());
  kubectl('-n lab-dev scale deploy/lab-drawio-dev-lab --replicas=1 >/dev/null && kubectl -n lab-dev rollout status deploy/lab-drawio-dev-lab --timeout=120s >/dev/null');
  await page.waitForTimeout(2000);
  const back = await ctx.request.get(BASE + '/editor/', { failOnStatusCode: false });
  check('recovers when draw.io is back', back.status() === 200, 'status ' + back.status());
  await ctx.close();
}

// 7. no bypass: draw.io has no public route of its own
{
  const svc = kubectl('-n lab-dev get svc lab-drawio-dev-lab -o jsonpath="{.spec.type}"').trim();
  const ing = kubectl('get ingress -A -o jsonpath="{range .items[*]}{.metadata.name}={.spec.rules[*].http.paths[*].backend.service.name}{\\"\\\\n\\"}{end}"');
  const drawioIngress = ing.split('\n').filter(l => l.includes('lab-drawio-dev-lab'));
  check('official draw.io is ClusterIP with no Ingress of its own', svc === 'ClusterIP' && drawioIngress.length === 0, `type=${svc} ingresses-to-drawio=${drawioIngress.length}`);
}
await browser.close();
finish();
