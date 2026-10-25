// UI behaviour reported from real use: Enter creates a diagram, the Share dialog separates sharing from AI settings,
// the History panel follows the diagram live. Run: node e2e/phase8_ui.mjs
import { BASE, check, finish, launch, newPage, keycloakLogin } from './lib.mjs';
const browser = await launch();
async function login(user) {
  const s = await newPage(browser);
  await s.page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  await keycloakLogin(s.page, user);
  await s.page.waitForURL(BASE + '/', { timeout: 30000 });
  await s.page.waitForSelector('#list-view:not([hidden])');
  s.csrf = (await s.page.evaluate(() => fetch('/api/me').then(r => r.json()))).csrf;
  return s;
}
const api = (s, m, p, body) => s.page.evaluate(async ([m, p, body, c]) => (await fetch('/api/v1' + p, { method: m, headers: { 'X-CSRF-Token': c, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })).json().catch(() => null), [m, p, body, s.csrf]);
const alice = await login('e2e-team1'), bob = await login('lab-test2');

// 1. New diagram: type the title and press Enter
await alice.page.click('#new');
await alice.page.waitForSelector('#dlg[open] input');
await alice.page.fill('#dlg input', 'Created with Enter');
await alice.page.keyboard.press('Enter');
await alice.page.waitForURL(/\/d\/[a-z0-9]+$/, { timeout: 15000 }).then(() => check('NEW DIAGRAM: title + Enter creates the diagram and opens it', true), () => check('NEW DIAGRAM: title + Enter creates the diagram and opens it', false, alice.page.url()));
const id = alice.page.url().split('/d/')[1];
await alice.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status').dataset.state === 'ready', null, { timeout: 60000 });
check('NEW DIAGRAM: the title is the one typed', (await alice.page.inputValue('#title')) === 'Created with Enter');

// 2. Share dialog
await alice.page.click('#btn-share');
await alice.page.waitForSelector('#dlg[open] #share-q');
const footer = await alice.page.$$eval('#dlg-actions button', bs => bs.map(b => b.textContent.trim() + (b.classList.contains('primary') ? '*' : '')));
check('SHARE: the primary button is "Done" (not "Save AI policy"), AI settings have their own save button', footer.join() === 'Done*' && await alice.page.isVisible('#save-ai') && await alice.page.isDisabled('#save-ai'), footer.join());
await alice.page.fill('#share-q', 'lab-test2');
await alice.page.waitForSelector('.hit[data-user="lab-test2"]', { timeout: 8000 });
await alice.page.click('.hit[data-user="lab-test2"]');
await alice.page.waitForSelector('[data-remove="lab-test2"]', { timeout: 8000 });
check('SHARE: picking a person shares immediately, and the row shows the name next to the role and Remove', (await alice.page.textContent('#share-list')).includes('lab-test2'));
await alice.page.fill('#share-q', 'e2e'); await alice.page.keyboard.press('Enter');
check('SHARE: Enter in the search box does not close the dialog', await alice.page.isVisible('#dlg[open] #share-q'));
await alice.page.check('#ai-allowed');
check('SHARE: changing AI settings enables their own Save button', await alice.page.isEnabled('#save-ai'));
await alice.page.click('#save-ai');
await alice.page.waitForFunction(() => document.getElementById('save-ai').disabled, null, { timeout: 5000 });
check('SHARE: AI policy saved on the server', (await api(alice, 'GET', `/documents/${id}`)).document.aiPolicy.allowed === true);
await alice.page.click('#dlg-actions .primary');
check('SHARE: Done closes the dialog', !(await alice.page.isVisible('#dlg[open]')));

// 3. History follows live
await bob.page.goto(BASE + '/d/' + id);
await bob.page.waitForFunction(() => window.__adapter && window.__adapter.state().joined && document.getElementById('status').dataset.state === 'ready', null, { timeout: 60000 });
await alice.page.evaluate(() => window.__app.openPanel('history'));
await alice.page.waitForSelector('#panel-body');
const count = () => alice.page.$$eval('#panel-body .item', x => x.length);
const before = await count();
await bob.page.evaluate(() => { const g = window.__adapter.editorUi().editor.graph; g.getModel().beginUpdate(); try { g.insertVertex(g.getDefaultParent(), 'h1', 'live history', 40, 40, 100, 40); } finally { g.getModel().endUpdate(); } });
const grew = await alice.page.waitForFunction(n => document.querySelectorAll('#panel-body .item').length > n, before, { timeout: 6000 }).then(() => true).catch(() => false);
check('HISTORY: a change by another user appears in the open History panel without reloading', grew, `${before} -> ${await count()} items`);
await api(alice, 'DELETE', `/documents/${id}`);
await browser.close();
finish();
