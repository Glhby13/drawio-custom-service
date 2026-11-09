// Trash / Restore E2E against the lab (ADR-0018): the list icon, the confirmation dialog, the Trash filter (?scope=trash), Restore,
// who may delete (owner / app admin, ADR-0019), the yellow Shared chip, the filters, and that a deleted diagram is gone for everybody else.
// Run: node e2e/phase12_trash.mjs   (needs the platform + gateway with the ADR-0018 change; lab test users from lab/SECRETS.md)
import { shareViaInvitation, BASE, check, finish, launch, newPage, keycloakLogin } from './lib.mjs';

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
  return { status: r.status, json: j };
}, { method, path, body, csrf: s.csrf });
const inList = (r, id) => (r.json && r.json.documents || []).some(d => d.id === id);
const row = (s, title) => s.page.locator('.doc', { hasText: title });

const owner = await login('e2e-team1');
const editor = await login('lab-test');
const viewer = await login('e2e-user2');
const stranger = await login('e2e-team2'); // has no share on the diagram
const tag = String(Date.now()).slice(-7), title = 'Trash E2E ' + tag;

// ---- set up: a diagram owned by e2e-team1, shared with an editor and a viewer
const made = await api(owner, 'POST', '/documents', { title });
const id = made.json && made.json.document && made.json.document.id;
check('owner creates a diagram', made.status === 201 && id, 'status ' + made.status);
await shareViaInvitation(api, owner, id, editor, 'lab-test', 'editor');
await shareViaInvitation(api, owner, id, viewer, 'e2e-user2', 'viewer');

// ---- UI: the owner sees the trash icon, the editor and the viewer do not
await owner.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
await owner.page.waitForSelector('#docs .doc');
const ownerBtn = row(owner, title).locator('button[title="Move to Trash"]');
check('owner sees the Move-to-Trash icon button with an accessible name', (await ownerBtn.count()) === 1 && /Move .* to Trash/.test((await ownerBtn.getAttribute('aria-label')) || ''), await ownerBtn.getAttribute('aria-label'));
for (const [name, s] of [['editor', editor], ['viewer', viewer]]) {
  await s.page.goto(BASE + '/?folder=shared', { waitUntil: 'domcontentloaded' }); // shared diagrams sit in the virtual "Shared with me" folder (ADR-0020)
  await s.page.waitForSelector('#docs .doc');
  check(name + ' sees the diagram but NO trash icon', (await row(s, title).count()) === 1 && (await row(s, title).locator('button[title="Move to Trash"]').count()) === 0);
}
// ---- ADR-0019: the button lives inside the card; the owner sees a yellow Shared chip that lists who has access; sharees do not
check('the Move-to-Trash button is inside the same card as the title', (await row(owner, title).locator('.doc-title').count()) === 1 && (await ownerBtn.count()) === 1);
const chip = row(owner, title).locator('button.chip.shared');
check('the owner sees a yellow "Shared · 2" chip', (await chip.count()) === 1 && /Shared · 2/.test(await chip.innerText()), await chip.allInnerTexts());
await chip.click();
await owner.page.waitForSelector('#dlg[open]');
const shareTxt = await owner.page.locator('#dlg-body .share-list').innerText();
check('clicking it lists the people and roles', /lab-test/.test(shareTxt) && /e2e-user2/.test(shareTxt) && /editor/i.test(shareTxt) && /viewer/i.test(shareTxt), shareTxt);
check('opening the chip did not navigate away', owner.page.url() === BASE + '/');
await owner.page.locator('#dlg-actions .btn', { hasText: 'Close' }).click();
await owner.page.waitForFunction(() => !document.getElementById('dlg').open);
check('a sharee sees no Shared chip (and no share list in the API)', (await row(editor, title).locator('button.chip.shared').count()) === 0 && !(((await api(editor, 'GET', '/documents')).json.documents.find(d => d.id === id) || {}).sharedWith));
await owner.page.selectOption('#scope', 'mine');
check('"Owned by me" keeps it, "Shared with me" hides it for the owner', (await row(owner, title).count()) === 1);
await owner.page.selectOption('#scope', 'shared');
check('the owner does not find it under "Shared with me"', (await row(owner, title).count()) === 0);
await editor.page.selectOption('#scope', 'shared');
check('the editor finds it under "Shared with me"', (await row(editor, title).count()) === 1);
await owner.page.selectOption('#scope', 'all');
await row(owner, title).waitFor();
check('an editor cannot delete through the API (403)', (await api(editor, 'DELETE', '/documents/' + id)).status === 403);
check('a viewer cannot delete through the API (403)', (await api(viewer, 'DELETE', '/documents/' + id)).status === 403);

// ---- UI: delete asks for confirmation; Cancel keeps it; confirm moves it to the Trash
await ownerBtn.click();
await owner.page.waitForSelector('#dlg[open]');
check('a confirmation dialog opens (nothing is deleted yet)', /Move to Trash/.test(await owner.page.locator('#dlg-title').innerText()) && (await row(owner, title).count()) === 1);
await owner.page.locator('#dlg-actions .btn', { hasText: 'Cancel' }).click();
await owner.page.waitForFunction(() => !document.getElementById('dlg').open);
check('Cancel keeps the diagram', (await row(owner, title).count()) === 1 && (await api(owner, 'GET', '/documents/' + id)).status === 200);
await ownerBtn.click();
await owner.page.waitForSelector('#dlg[open]');
await owner.page.locator('#dlg-actions .btn.primary').click();
await owner.page.waitForFunction(t => ![...document.querySelectorAll('.doc')].some(r => r.textContent.includes(t)), title, { timeout: 15000 });
check('confirming moves it out of the normal list', (await row(owner, title).count()) === 0);
check('the sharee list drops it entirely (not "trashed" for them)', !inList(await api(editor, 'GET', '/documents'), id) && !inList(await api(editor, 'GET', '/trash'), id));
check('the deleted diagram is gone for the owner (404) and for the editor (404)', (await api(owner, 'GET', '/documents/' + id)).status === 404 && (await api(editor, 'GET', '/documents/' + id)).status === 404);

// ---- UI: the Trash filter (state in the URL) lists it with a Restore button
await owner.page.selectOption('#scope', 'trash');
await row(owner, title).waitFor({ timeout: 15000 }); // the Trash list is fetched on demand
check('the filter is reflected in the URL (?scope=trash)', owner.page.url().includes('scope=trash'), owner.page.url());
check('the Trash lists the deleted diagram with Restore Diagram', (await row(owner, title).locator('button', { hasText: 'Restore Diagram' }).count()) === 1);
check('the editor does not see it in their Trash, the AI/other users have none of it', !inList(await api(editor, 'GET', '/trash'), id) && !inList(await api(viewer, 'GET', '/trash'), id));
await owner.page.reload({ waitUntil: 'domcontentloaded' });
await owner.page.waitForSelector('#docs .doc');
check('reload keeps the Trash view (deep link)', (await owner.page.locator('#scope').inputValue()) === 'trash' && (await row(owner, title).count()) === 1);
check('an editor cannot restore (404) and a stranger gets no existence oracle (404)', (await api(editor, 'POST', '/documents/' + id + '/undelete')).status === 404 && (await api(stranger, 'POST', '/documents/' + id + '/undelete')).status === 404 && !inList(await api(stranger, 'GET', '/trash'), id));

// ---- UI: Restore brings it back, shares included
await row(owner, title).locator('button', { hasText: 'Restore Diagram' }).click();
await owner.page.waitForFunction(t => ![...document.querySelectorAll('.doc')].some(r => r.textContent.includes(t)), title, { timeout: 15000 });
check('Restore removes it from the Trash view', (await row(owner, title).count()) === 0);
check('the restored diagram opens again for owner and editor (shares survived)', (await api(owner, 'GET', '/documents/' + id)).status === 200 && (await api(editor, 'GET', '/documents/' + id)).status === 200);
await owner.page.selectOption('#scope', 'all');
await row(owner, title).waitFor({ timeout: 15000 }); // the normal list is fetched again after leaving the Trash
check('and it is back in the normal list', (await row(owner, title).count()) === 1 && !owner.page.url().includes('scope='));
check('restoring a diagram that is not in the Trash is 404', (await api(owner, 'POST', '/documents/' + id + '/undelete')).status === 404);

// The application-admin path (admin deletes and restores any diagram) needs a TOTP login, and the lab has no stored OTP seed for
// e2e-drawio-admin, so it is covered by the integration test TestIT_TrashAndRestore instead of this browser suite.

// ---- editor menu: File > Move to Trash… (inside draw.io's own menu bar)
try {
  await owner.page.goto(BASE + '/d/' + id, { waitUntil: 'domcontentloaded' });
  const frame = owner.page.frameLocator('#editor');
  await frame.locator('.geMenubar a, .geMenubarContainer a').filter({ hasText: /^File$/ }).first().click({ timeout: 30000 });
  const item = frame.locator('td, div').filter({ hasText: /^Move to Trash…$/ }).first();
  check('File menu offers "Move to Trash…" to the owner', (await item.count()) > 0, '');
} catch (e) { check('File menu offers "Move to Trash…" to the owner', false, String(e).slice(0, 160)); }

await api(owner, 'DELETE', '/documents/' + id); // leave the lab tidy: the test diagram ends in the Trash
await browser.close();
finish();
