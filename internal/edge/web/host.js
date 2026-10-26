// Host application: document list, editor shell (share, history, versions, comments, export, presence).
// Rendering uses textContent / DOM builders only (the CSP forbids inline script and style attributes).
(function () {
  'use strict';
  var $ = function (id) { return document.getElementById(id); };
  var statusEl = $('status'), csrf = '', me = null, adapter = null, doc = null, editorUi = null;
  // Features that exist in the code and the API but are switched off in the UI for now (comments: not useful yet; flip to re-enable).
  var FEATURES = { comments: false };
  var THEME_KEY = 'drawspace_theme';

  function applyHostTheme(mode, save) {
    var m = mode === 'dark' ? 'dark' : 'light';
    document.documentElement.setAttribute('data-theme', m);
    if (save) {
      try { localStorage.setItem(THEME_KEY, m); } catch (e) { /* storage disabled */ }
    }
    var btnDay = $('theme-day'), btnNight = $('theme-night');
    if (btnDay) {
      btnDay.classList.toggle('active', m === 'light');
      btnDay.setAttribute('aria-pressed', String(m === 'light'));
    }
    if (btnNight) {
      btnNight.classList.toggle('active', m === 'dark');
      btnNight.setAttribute('aria-pressed', String(m === 'dark'));
    }
    if (editorUi && typeof editorUi.setDarkMode === 'function') {
      try { editorUi.setDarkMode(m === 'dark'); } catch (e) { /* ignore */ }
    }
  }
  (function initTheme() {
    var saved = 'light';
    try { saved = localStorage.getItem(THEME_KEY) || 'light'; } catch (e) { /* ignore */ }
    applyHostTheme(saved === 'dark' ? 'dark' : 'light', false);
  })();

  function setStatus(state, text) { statusEl.dataset.state = state; $('status-text').textContent = text; }
  function initials(name) {
    var parts = String(name || '').trim().split(/[\s._@-]+/).filter(Boolean);
    if (!parts.length) return '?';
    return (parts.length === 1 ? parts[0].slice(0, 2) : parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
  }
  function el(tag, props, kids) {
    var e = document.createElement(tag);
    Object.keys(props || {}).forEach(function (k) {
      if (k === 'class') e.className = props[k];
      else if (k === 'text') e.textContent = props[k];
      else if (k.slice(0, 2) === 'on') e.addEventListener(k.slice(2), props[k]);
      else if (props[k] !== false && props[k] != null) e.setAttribute(k, props[k] === true ? '' : props[k]);
    });
    (kids || []).forEach(function (c) { if (c != null) e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
    return e;
  }
  function toast(kind, text) {
    var t = el('div', { class: 'toast ' + (kind || ''), text: text, role: 'status' });
    $('toasts').appendChild(t);
    setTimeout(function () { t.remove(); }, kind === 'error' ? 8000 : 5000);
  }
  function ago(iso) {
    var s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
    if (s < 60) return 'just now';
    if (s < 3600) return Math.floor(s / 60) + ' min ago';
    if (s < 86400) return Math.floor(s / 3600) + ' h ago';
    return new Date(iso).toLocaleDateString();
  }

  function api(method, path, body) {
    var opt = { method: method, credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf } };
    if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
    return fetch('/api/v1' + path, opt).then(function (r) {
      return r.text().then(function (t) {
        var j = null;
        try { j = t ? JSON.parse(t) : null; } catch (e) { /* not json */ }
        if (!r.ok) {
          var err = new Error((j && j.error && (j.error.message || j.error.code)) || ('HTTP ' + r.status));
          err.status = r.status; err.code = j && j.error && j.error.code;
          throw err;
        }
        return j;
      });
    });
  }

  // ---- dialog ----------------------------------------------------------------------------------------------------------
  function dialog(title, body, actions, opts) {
    $('dlg-title').textContent = title;
    var b = $('dlg-body'), a = $('dlg-actions');
    b.textContent = ''; a.textContent = '';
    b.appendChild(body);
    actions.forEach(function (x) {
      a.appendChild(el('button', { class: 'btn ' + (x.cls || ''), type: 'button', text: x.label, onclick: function () { if (x.fn) { var r = x.fn(); if (r === false) return; } if (!x.keep) $('dlg').close(); } }));
    });
    var d = $('dlg');
    d.classList.toggle('dlg-wide', !!(opts && opts.wide));
    if (!d.open) d.showModal();
  }
  if ($('dlg-close')) {
    $('dlg-close').addEventListener('click', function () { $('dlg').close(); });
  }
  // Close dialog when clicking on the backdrop outside the dialog box
  $('dlg').addEventListener('mousedown', function (e) {
    if (e.target === $('dlg')) $('dlg').close();
  });
  // Enter inside a dialog field = the primary button (the <form method=dialog> would otherwise just close the dialog)
  $('dlg-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var p = $('dlg-actions').querySelector('.primary');
    if (p && !p.disabled) p.click();
  });
  function ask(title, text, okLabel) {
    return new Promise(function (resolve) {
      dialog(title, el('p', { text: text }), [{ label: 'Cancel', fn: function () { resolve(false); } }, { label: okLabel || 'OK', cls: 'primary', fn: function () { resolve(true); } }]);
      $('dlg').addEventListener('close', function once() { $('dlg').removeEventListener('close', once); resolve(false); });
    });
  }
  function prompt2(title, label, value) {
    return new Promise(function (resolve) {
      var input = el('input', { class: 'field', type: 'text', maxlength: '200', value: value || '' });
      input.value = value || '';
      dialog(title, el('label', null, [label, input]), [{ label: 'Cancel', fn: function () { resolve(null); } }, { label: 'OK', cls: 'primary', fn: function () { resolve(input.value.trim()); } }]);
      setTimeout(function () { input.focus(); input.select(); }, 30);
      $('dlg').addEventListener('close', function once() { $('dlg').removeEventListener('close', once); resolve(null); });
    });
  }

  // ---- list view -------------------------------------------------------------------------------------------------------
  var allDocs = [];
  function showList() {
    $('list-view').hidden = false;
    setStatus('auth', 'Signed in');
    api('GET', '/documents').then(function (r) { allDocs = r.documents || []; renderList(); }).catch(function (e) { setStatus('error', 'Could not load diagrams'); toast('error', e.message); });
    $('new').onclick = function () {
      prompt2('New diagram', 'Title', 'Untitled diagram').then(function (t) {
        if (t === null) return;
        api('POST', '/documents', { title: t || 'Untitled diagram' }).then(function (r) { location.href = '/d/' + r.document.id; }).catch(function (e) { toast('error', e.message); });
      });
    };
    $('import').onclick = function () { $('import-file').click(); };
    $('import-file').onchange = function () {
      var f = this.files[0]; if (!f) return;
      var reader = new FileReader();
      reader.onload = function () {
        api('POST', '/documents', { title: f.name.replace(/\.(drawio|xml)$/i, ''), xml: String(reader.result) })
          .then(function (r) { location.href = '/d/' + r.document.id; })
          .catch(function (e) { toast('error', 'Import failed: ' + e.message); });
      };
      reader.readAsText(f);
    };
    $('filter').oninput = renderList; $('scope').onchange = renderList;
  }
  function renderList() {
    var q = $('filter').value.trim().toLowerCase(), scope = $('scope').value, box = $('docs');
    box.textContent = '';
    var rows = allDocs.filter(function (d) {
      if (q && d.title.toLowerCase().indexOf(q) < 0) return false;
      if (scope === 'mine') return d.owner === me.username;
      if (scope === 'shared') return d.owner !== me.username;
      return true;
    });
    rows.forEach(function (d) {
      var chips = [el('span', { class: 'chip', text: d.role })];
      if (d.aiPolicy && d.aiPolicy.allowed) chips.push(el('span', { class: 'chip ai', text: 'AI allowed' }));
      box.appendChild(el('a', { class: 'doc', href: '/d/' + d.id, role: 'listitem' }, [
        el('span', { class: 'doc-title', text: d.title }),
        el('span', null, chips),
        el('span', { class: 'doc-meta', text: 'Owner ' + d.owner + ' · updated ' + ago(d.updatedAt) + ' · revision ' + d.headSeq }),
      ]));
    });
    $('empty').hidden = rows.length > 0;
  }

  // ---- editor view -----------------------------------------------------------------------------------------------------
  function showEditor(id) {
    $('editor-view').hidden = false;
    api('GET', '/documents/' + encodeURIComponent(id)).then(function (r) {
      doc = r;
      $('title').value = r.document.title;
      $('docinfo').hidden = false;
      document.title = r.document.title + ' · DrawSpace';
      applyRole(r.role);
      var frame = $('editor');
      frame.hidden = false;
      frame.src = '/editor/?embed=1&proto=json&spin=1&ui=atlas&noSaveBtn=1&noExitBtn=1&saveAndExit=0&libraries=1';
      adapter = window.DrawioAdapter.create({
        frame: frame, docId: id,
        onStatus: setStatus,
        onRole: function (role, title) { applyRole(role); if (title && title !== $('title').value && document.activeElement !== $('title')) $('title').value = title; },
        onPresence: renderPresence,
        onToast: toast,
        onFatal: function (code, msg) { setStatus('error', 'Disconnected'); var b = $('banner'); b.textContent = msg; b.hidden = false; frame.hidden = true; },
        onSeq: function () { refreshPanelSoon(); },
        onEditorReady: function (ui, win) { installEditorMenus(ui, win, id); },
      });
      window.__adapter = adapter; // used by the end-to-end suite
      window.__app = { openPanel: function (kind) { openKind(kind, id); }, openAi: function () { openAiSettings(id); }, dirty: unsaved };
    }).catch(function (e) {
      setStatus('error', 'Unavailable');
      var b = $('banner'); b.textContent = e.status === 404 ? 'This diagram does not exist or is not shared with you.' : e.message; b.hidden = false;
      $('btn-share').hidden = true;
    });

    $('title').onchange = function () {
      var t = this.value.trim(); if (!t) { this.value = doc.document.title; return; }
      api('PATCH', '/documents/' + id, { title: t }).then(function () { doc.document.title = t; document.title = t + ' · DrawSpace'; }).catch(function (e) { toast('error', e.message); $('title').value = doc.document.title; });
    };
    $('title').addEventListener('keydown', function (e) { if (e.key === 'Enter') this.blur(); });
    $('btn-share').onclick = function () { openShare(id); };
    $('panel-close').onclick = function () { $('panel').hidden = true; panelRender = null; panelKind = ''; clearInterval(panelPoll); };
    window.addEventListener('beforeunload', function (e) {
      if (unsaved()) { e.preventDefault(); e.returnValue = ''; return ''; } // only when something has not reached the server yet
      if (adapter) adapter.close();
    });
  }

  function openKind(kind, id) {
    if (kind === 'history') openPanel('History', id, renderHistory);
    else if (kind === 'versions') openPanel('Versions', id, renderVersions);
    else if (kind === 'comments' && FEATURES.comments) openPanel('Comments', id, renderComments);
  }

  // Our functions live INSIDE draw.io's own menu bar (one bar, no second toolbar): "History" and "AI" to the right of "Help", plus copy-as-image entries.
  function installEditorMenus(ui, win, id) {
    editorUi = ui;
    // Close host dropdowns when the user clicks inside the draw.io iframe
    if (win && win.document) {
      win.document.addEventListener('mousedown', function () {
        closeMenu();
      }, true);
    }
    // Sync draw.io dark mode with saved DrawSpace theme if supported
    if (typeof ui.setDarkMode === 'function') {
      try { ui.setDarkMode(document.documentElement.getAttribute('data-theme') === 'dark'); } catch (e) { /* ignore */ }
    }
    // Optional: remove the orange draw.io app icon to the left of File without touching the draw.io image
    var appIcon = ui.menubar.container.querySelector('.geSmallAppIcon');
    if (appIcon) appIcon.remove();

    var Menu = win.Menu, canEdit = function () { return doc && doc.can && doc.can.edit; };
    Array.prototype.slice.call(ui.menubar.container.querySelectorAll('[data-drawspace-menu]')).forEach(function (n) { n.remove(); });
    ui.menus.put('drawspace', new Menu(function (menu, parent) {
      menu.addItem('Change history', null, function () { openKind('history', id); }, parent);
      menu.addItem('Named versions', null, function () { openKind('versions', id); }, parent);
      if (canEdit()) menu.addItem('Save version…', null, function () { saveVersionDialog(id); }, parent);
      if (FEATURES.comments) menu.addItem('Comments', null, function () { openKind('comments', id); }, parent);
    }));
    var elt = ui.menubar.addMenu('History', ui.menus.get('drawspace').funct);
    elt.className = 'geItem';
    elt.setAttribute('data-drawspace-menu', 'history');
    var aiElt = ui.menubar.container.ownerDocument.createElement('a');
    aiElt.className = 'geItem';
    aiElt.setAttribute('data-drawspace-menu', 'ai');
    aiElt.textContent = 'AI';
    aiElt.addEventListener('click', function (e) {
      e.preventDefault();
      e.stopPropagation();
      if (ui.hideCurrentMenu) ui.hideCurrentMenu();
      openAiSettings(id);
    });
    var statusAnchor = ui.menubar.container.querySelector('.geStatus');
    var help = Array.prototype.find.call(ui.menubar.container.children, function (c) { return c !== elt && /^help$/i.test((c.textContent || '').trim()); });
    var afterHelp = help ? help.nextSibling : statusAnchor;
    if (afterHelp) {
      ui.menubar.container.insertBefore(elt, afterHelp);
      ui.menubar.container.insertBefore(aiElt, afterHelp);
    } else {
      ui.menubar.container.appendChild(elt);
      ui.menubar.container.appendChild(aiElt);
    }
    // copy the selection as an image (the clipboard accepts PNG and SVG only; JPG is saved as a file through draw.io's own export dialog)
    var graph = ui.editor.graph, act = function (name) { return function () { var a = ui.actions.get(name); if (a) a.funct(); }; };
    function addCopyAs(menu, parent) {
      menu.addSeparator(parent);
      menu.addItem('Copy as PNG image', null, act('copyAsImage'), parent);
      menu.addItem('Copy as SVG', null, act('copyAsSvg'), parent);
      menu.addItem('Save selection as JPG…', null, act('exportJpg'), parent);
    }
    var origPopup = ui.menus.createPopupMenu;
    ui.menus.createPopupMenu = function (menu, cell, evt) {
      origPopup.apply(this, arguments);
      if (graph.getSelectionCount() > 0) addCopyAs(menu, null);
    };
    var edit = ui.menus.get('edit'), origEdit = edit.funct;
    edit.funct = function (menu, parent) { origEdit.apply(this, arguments); addCopyAs(menu, parent); };
  }

  function saveVersionDialog(id) {
    prompt2('Save version', 'Name', '').then(function (n) {
      if (!n) return;
      api('POST', '/documents/' + id + '/versions', { name: n }).then(function () { toast('info', 'Version saved'); if (panelKind === 'versions' && panelRender) panelRender(); }).catch(function (e) { toast('error', e.message); });
    });
  }

  // ---- unsaved work guard: no browser pop-up when everything is saved --------------------------------------------------------------------------------
  function unsaved() { return !!(adapter && adapter.state && adapter.state().dirty); }
  function guardedNavigate(go) {
    if (!unsaved()) { go(); return; }
    ask('Changes are still being saved', 'Your latest changes have not reached the server yet. If you leave now they may be lost.', 'Leave anyway').then(function (ok) { if (ok) { if (adapter) adapter.close(); go(); } });
  }
  document.addEventListener('click', function (e) {
    var a = e.target.closest && e.target.closest('a[href]');
    if (!a || a.target === '_blank' || e.defaultPrevented || e.metaKey || e.ctrlKey || e.shiftKey || a.origin !== location.origin) return;
    if (!unsaved()) return;
    e.preventDefault();
    guardedNavigate(function () { location.href = a.href; });
  });

  function applyRole(role) {
    var can = doc && doc.can || {};
    var edit = role === 'editor' || role === 'owner';
    var badge = $('rolebadge');
    badge.hidden = false; badge.dataset.role = role; badge.textContent = role === 'owner' ? 'owner' : edit ? 'can edit' : role === 'commenter' ? 'can comment' : 'view only';
    $('title').readOnly = !(role === 'owner' || can.rename);
    $('btn-share').hidden = !(role === 'owner' || can.share);
    if (panelRender) panelRender();
  }

  var presenceTimer = null;
  function renderPresence(list, you) {
    var box = $('presence'); box.textContent = '';
    var seen = {}, now = Date.now(), next = 0;
    clearTimeout(presenceTimer);
    list = list.filter(function (p) {
      if (!p.exp) return true;
      if (p.exp * 1000 <= now) return false;
      next = next ? Math.min(next, p.exp * 1000) : p.exp * 1000; // an AI actor working through the API stays visible for a short while
      return true;
    });
    if (next) presenceTimer = setTimeout(function () { renderPresence(list, you); }, next - now + 200);
    list.forEach(function (p) {
      var key = p.user + ':' + p.type + ':' + (p.agent || '');
      if (seen[key]) return; seen[key] = true;
      var label = (p.type === 'ai' ? '🤖 ' + (p.agent || p.user) + (p.obo ? ' (for ' + p.obo + ')' : '') : (p.name || p.user)) + ' · ' + p.role;
      var av = el('span', { class: 'pav' + (p.type === 'ai' ? ' ai' : '') + (p.sid === you ? ' me' : ''), title: label, 'data-user': p.user, text: p.type === 'ai' ? 'AI' : initials(p.name || p.user) });
      box.appendChild(av);
    });
  }

  // ---- side panel ------------------------------------------------------------------------------------------------------
  var panelRender = null, panelKind = '', panelTimer = 0, panelPoll = 0;
  // History / Versions follow the diagram live (every sequenced operation refreshes the open panel, debounced); Comments poll.
  function refreshPanelSoon() {
    if (!panelRender || panelKind === 'comments' || panelKind === '') return;
    clearTimeout(panelTimer);
    panelTimer = setTimeout(function () { if (panelRender) panelRender(); }, 600);
  }
  function openPanel(title, id, fn) {
    $('panel').hidden = false; $('panel-title').textContent = title; panelKind = title.toLowerCase();
    clearInterval(panelPoll);
    if (panelKind === 'comments') panelPoll = setInterval(function () { if (panelRender && !document.hidden && !document.activeElement.matches('textarea')) panelRender(); }, 6000);
    panelRender = function () { fn(id, $('panel-body')); };
    panelRender();
  }
  function itemRow(title, sub, buttons) {
    return el('div', { class: 'item' }, [el('div', { class: 'row' }, [el('span', { class: 'grow', text: title })].concat(buttons || [])), el('small', { text: sub })]);
  }
  function opPill(type) {
    var cls = '';
    var prefix = '• ';
    if (type === 'addCell' || type === 'addPage') { cls = 'add'; prefix = '+ '; }
    else if (type === 'delCell' || type === 'delPage') { cls = 'del'; prefix = '− '; }
    else if (type === 'setProp' || type === 'setPos' || type === 'renamePage' || type === 'orderPage' || type === 'setTitle') { cls = 'mod'; prefix = '✎ '; }
    else if (type === 'connect') { cls = 'conn'; prefix = '↗ '; }
    return el('span', { class: 'op-pill ' + cls, text: prefix + type });
  }
  function stripHtml(s) {
    return String(s || '').replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim();
  }
  function parseStyleMap(styleStr) {
    var m = {};
    String(styleStr || '').split(';').forEach(function (part) {
      var idx = part.indexOf('=');
      if (idx > 0) m[part.slice(0, idx).trim()] = part.slice(idx + 1).trim();
      else if (part.trim()) m[part.trim()] = '1';
    });
    return m;
  }
  function buildCellLookup(pages) {
    var map = {};
    (pages || []).forEach(function (pg) {
      (pg.cells || []).forEach(function (c) {
        map[c.id] = { page: pg.name || pg.id, cell: c, props: c.props || {} };
      });
    });
    return map;
  }
  function renderDiagramPreviewSvg(pages, highlightMap, deletedCells) {
    var svgNS = 'http://www.w3.org/2000/svg';
    var svg = document.createElementNS(svgNS, 'svg');
    var pg = (pages && pages[0]) || { cells: [] };
    var cells = (pg.cells || []).slice();
    (deletedCells || []).forEach(function (dc) {
      if (!cells.some(function (c) { return c.id === dc.id; })) cells.push(dc);
    });
    var vertices = {}, edges = [];
    var minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    cells.forEach(function (c) {
      var p = c.props || {};
      if (c.k === 'v') {
        var x = parseFloat(p.x), y = parseFloat(p.y), w = parseFloat(p.w), h = parseFloat(p.h);
        if (isNaN(x)) x = 40;
        if (isNaN(y)) y = 40;
        if (isNaN(w) || w <= 0) w = 120;
        if (isNaN(h) || h <= 0) h = 60;
        vertices[c.id] = { id: c.id, x: x, y: y, w: w, h: h, props: p };
        if (x < minX) minX = x;
        if (y < minY) minY = y;
        if (x + w > maxX) maxX = x + w;
        if (y + h > maxY) maxY = y + h;
      } else if (c.k === 'e') {
        edges.push({ id: c.id, props: p });
      }
    });
    if (!isFinite(minX)) { minX = 0; minY = 0; maxX = 560; maxY = 260; }
    var pad = 36;
    var vw = Math.max(420, (maxX - minX) + pad * 2);
    var vh = Math.max(220, (maxY - minY) + pad * 2);
    svg.setAttribute('viewBox', (minX - pad) + ' ' + (minY - pad) + ' ' + vw + ' ' + vh);

    var defs = document.createElementNS(svgNS, 'defs');
    var marker = document.createElementNS(svgNS, 'marker');
    marker.setAttribute('id', 'pv-arrow');
    marker.setAttribute('viewBox', '0 0 10 10');
    marker.setAttribute('refX', '8');
    marker.setAttribute('refY', '5');
    marker.setAttribute('markerWidth', '6');
    marker.setAttribute('markerHeight', '6');
    marker.setAttribute('orient', 'auto-start-reverse');
    var mpath = document.createElementNS(svgNS, 'path');
    mpath.setAttribute('d', 'M 0 1 L 10 5 L 0 9 z');
    mpath.setAttribute('fill', '#64748b');
    marker.appendChild(mpath);
    defs.appendChild(marker);
    svg.appendChild(defs);

    // Draw edges first
    edges.forEach(function (e) {
      var s = vertices[e.props.source], t = vertices[e.props.target];
      var x1 = s ? s.x + s.w / 2 : minX + 40;
      var y1 = s ? s.y + s.h / 2 : minY + 40;
      var x2 = t ? t.x + t.w / 2 : x1 + 100;
      var y2 = t ? t.y + t.h / 2 : y1;
      var hl = highlightMap && highlightMap[e.id];
      var stroke = hl === 'added' ? '#16a34a' : hl === 'deleted' ? '#dc2626' : hl === 'modified' ? '#2563eb' : '#64748b';
      var line = document.createElementNS(svgNS, 'line');
      line.setAttribute('x1', String(x1));
      line.setAttribute('y1', String(y1));
      line.setAttribute('x2', String(x2));
      line.setAttribute('y2', String(y2));
      line.setAttribute('stroke', stroke);
      line.setAttribute('stroke-width', hl ? '2.5' : '1.75');
      if (hl === 'deleted') line.setAttribute('stroke-dasharray', '5,4');
      line.setAttribute('marker-end', 'url(#pv-arrow)');
      svg.appendChild(line);
    });

    // Draw vertices
    Object.keys(vertices).forEach(function (id) {
      var v = vertices[id];
      var st = parseStyleMap(v.props.style);
      var fill = st.fillColor && st.fillColor !== 'none' ? st.fillColor : '#ffffff';
      var stroke = st.strokeColor && st.strokeColor !== 'none' ? st.strokeColor : '#334155';
      var hl = highlightMap && highlightMap[id];
      var g = document.createElementNS(svgNS, 'g');
      if (hl) {
        var ring = document.createElementNS(svgNS, 'rect');
        ring.setAttribute('x', String(v.x - 4));
        ring.setAttribute('y', String(v.y - 4));
        ring.setAttribute('width', String(v.w + 8));
        ring.setAttribute('height', String(v.h + 8));
        ring.setAttribute('rx', '8');
        ring.setAttribute('fill', 'none');
        ring.setAttribute('stroke', hl === 'added' ? '#16a34a' : hl === 'deleted' ? '#dc2626' : '#2563eb');
        ring.setAttribute('stroke-width', '2.5');
        if (hl === 'deleted') ring.setAttribute('stroke-dasharray', '5,4');
        g.appendChild(ring);
      }
      var shape;
      if (st.ellipse) {
        shape = document.createElementNS(svgNS, 'ellipse');
        shape.setAttribute('cx', String(v.x + v.w / 2));
        shape.setAttribute('cy', String(v.y + v.h / 2));
        shape.setAttribute('rx', String(v.w / 2));
        shape.setAttribute('ry', String(v.h / 2));
      } else if (st.rhombus) {
        shape = document.createElementNS(svgNS, 'polygon');
        var cx = v.x + v.w / 2, cy = v.y + v.h / 2;
        shape.setAttribute('points', cx + ',' + v.y + ' ' + (v.x + v.w) + ',' + cy + ' ' + cx + ',' + (v.y + v.h) + ' ' + v.x + ',' + cy);
      } else {
        shape = document.createElementNS(svgNS, 'rect');
        shape.setAttribute('x', String(v.x));
        shape.setAttribute('y', String(v.y));
        shape.setAttribute('width', String(v.w));
        shape.setAttribute('height', String(v.h));
        if (st.rounded === '1') shape.setAttribute('rx', '8');
      }
      shape.setAttribute('fill', hl === 'deleted' ? '#fee2e2' : fill);
      shape.setAttribute('stroke', hl === 'deleted' ? '#dc2626' : stroke);
      shape.setAttribute('stroke-width', '1.5');
      if (hl === 'deleted') shape.setAttribute('opacity', '0.72');
      g.appendChild(shape);

      var label = stripHtml(v.props.value || '');
      if (label) {
        var txt = document.createElementNS(svgNS, 'text');
        txt.setAttribute('x', String(v.x + v.w / 2));
        txt.setAttribute('y', String(v.y + v.h / 2 + 4));
        txt.setAttribute('text-anchor', 'middle');
        txt.setAttribute('font-size', '12');
        txt.setAttribute('font-family', 'Inter, system-ui, sans-serif');
        txt.setAttribute('fill', '#0f172a');
        txt.textContent = label.length > 26 ? label.slice(0, 25) + '…' : label;
        g.appendChild(txt);
      }
      svg.appendChild(g);
    });
    return svg;
  }

  function describeBatchChanges(ops, stateBefore, stateAfter) {
    var beforeMap = buildCellLookup(stateBefore);
    var afterMap = buildCellLookup(stateAfter);
    var highlightMap = {};
    var deletedCells = [];
    var rows = [];

    (ops || []).forEach(function (op) {
      var t = op.op;
      var cid = op.id || op.page || '';
      var bCell = beforeMap[cid] && beforeMap[cid].props || {};
      var aCell = afterMap[cid] && afterMap[cid].props || {};
      var label = stripHtml(aCell.value || bCell.value || op.name || '') || cid;
      var detailKids = [];

      if (t === 'addCell') {
        highlightMap[cid] = 'added';
        var kind = op.kind === 'e' ? 'Connector' : 'Shape';
        detailKids.push(el('strong', { text: kind + ' "' + label + '"' }));
        if (aCell.x !== undefined && aCell.y !== undefined) {
          detailKids.push(' at (' + aCell.x + ', ' + aCell.y + '), size ' + (aCell.w || '—') + '×' + (aCell.h || '—'));
        }
      } else if (t === 'delCell') {
        highlightMap[cid] = 'deleted';
        if (beforeMap[cid] && beforeMap[cid].cell) deletedCells.push(beforeMap[cid].cell);
        detailKids.push(el('strong', { text: 'Removed "' + label + '"' }));
      } else if (t === 'setProp') {
        if (!highlightMap[cid]) highlightMap[cid] = 'modified';
        var oldVal = bCell[op.key] !== undefined ? stripHtml(bCell[op.key]) : '(empty)';
        var newVal = op.value !== undefined && op.value !== null ? stripHtml(op.value) : (aCell[op.key] !== undefined ? stripHtml(aCell[op.key]) : '(empty)');
        detailKids.push(el('strong', { text: '"' + label + '" · ' + (op.key || 'property') + ': ' }));
        detailKids.push(el('span', { class: 'diff-val-old', text: oldVal || '(empty)' }));
        detailKids.push(' → ');
        detailKids.push(el('span', { class: 'diff-val-new', text: newVal || '(empty)' }));
      } else if (t === 'setPos') {
        if (!highlightMap[cid]) highlightMap[cid] = 'modified';
        var oldPos = (bCell.x !== undefined ? '(' + bCell.x + ', ' + bCell.y + ') ' + (bCell.w || '') + '×' + (bCell.h || '') : 'previous');
        var newPos = '(' + (aCell.x !== undefined ? aCell.x : '') + ', ' + (aCell.y !== undefined ? aCell.y : '') + ') ' + (aCell.w || '') + '×' + (aCell.h || '');
        detailKids.push(el('strong', { text: 'Moved/resized "' + label + '": ' }));
        detailKids.push(el('span', { class: 'diff-val-old', text: oldPos }));
        detailKids.push(' → ');
        detailKids.push(el('span', { class: 'diff-val-new', text: newPos }));
      } else if (t === 'connect') {
        if (!highlightMap[cid]) highlightMap[cid] = 'modified';
        detailKids.push(el('strong', { text: 'Connected "' + label + '" (' + (op.end || 'end') + ' → ' + (op.target || 'none') + ')' }));
      } else {
        detailKids.push(el('strong', { text: t + (label ? ' · ' + label : '') }));
      }
      rows.push(el('div', { class: 'diff-row' }, [
        opPill(t),
        el('div', { class: 'diff-cell-text' }, detailKids),
      ]));
    });
    return { rows: rows, highlightMap: highlightMap, deletedCells: deletedCells };
  }

  function openHistoryPreview(id, h, body) {
    var who = h.actorType === 'ai' ? '🤖 ' + (h.agent || h.actor) + (h.onBehalfOf ? ' for ' + h.onBehalfOf : '') : h.actor;
    api('GET', '/documents/' + id + '/history/' + encodeURIComponent(h.batch)).then(function (detail) {
      var diff = describeBatchChanges(detail.ops || [], detail.stateBefore || [], detail.stateAfter || []);
      var wrap = el('div', { class: 'preview-wrap' }, [
        el('div', { class: 'preview-meta' }, [
          el('span', null, ['Actor: ', el('strong', { text: who })]),
          el('span', null, ['Revision: ', el('strong', { text: '#' + detail.fromSeq + (detail.toSeq !== detail.fromSeq ? '–#' + detail.toSeq : '') })]),
          el('span', null, ['When: ', el('strong', { text: ago(h.at) })]),
          el('span', null, ['Operations: ', el('strong', { text: String((detail.ops || []).length) })]),
        ]),
        el('div', { class: 'preview-canvas-box' }, [
          el('div', { class: 'preview-canvas-head' }, [
            el('span', { text: 'Diagram state at revision #' + detail.toSeq + ' (green = added, blue = modified, red dashed = removed)' }),
          ]),
          el('div', { class: 'preview-svg' }, [renderDiagramPreviewSvg(detail.stateAfter || [], diff.highlightMap, diff.deletedCells)]),
        ]),
        el('div', { class: 'preview-diff-list' }, diff.rows.length ? diff.rows : [el('small', { text: 'No cell-level details recorded.' })]),
      ]);
      var actions = [{ label: 'Close' }];
      if (doc && doc.can && doc.can.edit) {
        actions.push({
          label: 'Revert this change',
          cls: 'primary',
          fn: function () {
            api('POST', '/documents/' + id + '/revert', { batch: h.batch }).then(function (x) {
              toast('info', 'Reverted ' + x.applied + ' operation(s)' + (x.skipped ? ', skipped ' + x.skipped : ''));
              if (body) renderHistory(id, body);
            }).catch(function (e) { toast('error', e.message); });
          }
        });
      }
      dialog('Preview change #' + h.toSeq + ' — ' + who, wrap, actions, { wide: true });
    }).catch(function (e) { toast('error', e.message); });
  }

  function openVersionPreview(id, v) {
    api('GET', '/documents/' + id + '/versions/' + encodeURIComponent(v.id)).then(function (detail) {
      var pages = detail.state || [];
      var summaryRows = [];
      pages.forEach(function (pg) {
        (pg.cells || []).forEach(function (c) {
          var p = c.props || {};
          var lbl = stripHtml(p.value || '') || c.id;
          summaryRows.push(el('div', { class: 'diff-row' }, [
            opPill(c.k === 'e' ? 'connect' : 'addCell'),
            el('div', { class: 'diff-cell-text' }, [
              el('strong', { text: (c.k === 'e' ? 'Connector ' : 'Shape ') + '"' + lbl + '"' }),
              c.k === 'v' && p.x !== undefined ? ' at (' + p.x + ', ' + p.y + '), ' + (p.w || '—') + '×' + (p.h || '—') : '',
            ]),
          ]));
        });
      });
      var wrap = el('div', { class: 'preview-wrap' }, [
        el('div', { class: 'preview-meta' }, [
          el('span', null, ['Version: ', el('strong', { text: v.name })]),
          el('span', null, ['Revision: ', el('strong', { text: '#' + v.seq })]),
          el('span', null, ['Saved by: ', el('strong', { text: v.createdBy })]),
          el('span', null, ['When: ', el('strong', { text: ago(v.createdAt) })]),
        ]),
        el('div', { class: 'preview-canvas-box' }, [
          el('div', { class: 'preview-canvas-head' }, [
            el('span', { text: 'Visual snapshot of "' + v.name + '" (revision #' + v.seq + ')' }),
          ]),
          el('div', { class: 'preview-svg' }, [renderDiagramPreviewSvg(pages, null, null)]),
        ]),
        el('div', { class: 'preview-diff-list' }, summaryRows.length ? summaryRows : [el('small', { text: 'Empty diagram in this version.' })]),
      ]);
      var actions = [{ label: 'Close' }];
      if (doc && doc.can && doc.can.restore) {
        actions.push({
          label: 'Restore this version',
          cls: 'primary',
          fn: function () {
            api('POST', '/documents/' + id + '/versions/' + v.id + '/restore', {}).then(function () {
              toast('info', 'Version restored');
            }).catch(function (e) { toast('error', e.message); });
          }
        });
      }
      dialog('Preview version — "' + v.name + '"', wrap, actions, { wide: true });
    }).catch(function (e) { toast('error', e.message); });
  }

  function renderHistory(id, body) {
    body.textContent = 'Loading…';
    api('GET', '/documents/' + id + '/history?limit=60').then(function (r) {
      body.textContent = '';
      if (!r.history.length) body.appendChild(el('p', { text: 'No changes yet.' }));
      r.history.forEach(function (h) {
        var who = h.actorType === 'ai' ? '🤖 ' + (h.agent || h.actor) + (h.onBehalfOf ? ' for ' + h.onBehalfOf : '') : h.actor;
        var seqLabel = '#' + h.fromSeq + (h.toSeq && h.toSeq !== h.fromSeq ? '–#' + h.toSeq : '');
        var pills = (h.types || []).map(opPill);
        var btns = [
          el('button', { class: 'btn', type: 'button', text: 'Preview', 'data-preview-batch': h.batch, onclick: function (e) { e.stopPropagation(); openHistoryPreview(id, h, body); } }),
        ];
        if (doc.can && doc.can.edit) {
          btns.push(el('button', { class: 'btn', type: 'button', text: 'Revert', 'data-batch': h.batch, onclick: function (e) {
            e.stopPropagation();
            ask('Revert change', 'Undo this change by ' + who + '? Other later changes stay.', 'Revert').then(function (ok) {
              if (!ok) return;
              api('POST', '/documents/' + id + '/revert', { batch: h.batch }).then(function (x) { toast('info', 'Reverted ' + x.applied + ' operation(s)' + (x.skipped ? ', skipped ' + x.skipped : '')); renderHistory(id, body); }).catch(function (e) { toast('error', e.message); });
            });
          } }));
        }
        var card = el('div', { class: 'item', onclick: function () { openHistoryPreview(id, h, body); } }, [
          el('div', { class: 'row' }, [
            el('span', { class: 'hist-actor grow', text: who }),
            el('span', { class: 'hist-seq', text: seqLabel }),
          ]),
          el('div', { class: 'hist-ops' }, pills),
          el('div', { class: 'row' }, [
            el('small', { class: 'grow', text: h.ops + ' op(s) · ' + ago(h.at) }),
            el('div', { class: 'actions' }, btns),
          ]),
        ]);
        body.appendChild(card);
      });
    }).catch(function (e) { body.textContent = e.message; });
  }
  function renderVersions(id, body) {
    body.textContent = 'Loading…';
    api('GET', '/documents/' + id + '/versions').then(function (r) {
      body.textContent = '';
      if (doc.can && doc.can.edit) body.appendChild(el('button', { class: 'btn primary', type: 'button', id: 'save-version', text: 'Save current as version', onclick: function () { saveVersionDialog(id); } }));
      (r.versions || []).forEach(function (v) {
        var btns = [
          el('button', { class: 'btn', type: 'button', text: 'Preview', 'data-preview-version': v.id, onclick: function (e) { e.stopPropagation(); openVersionPreview(id, v); } }),
        ];
        if (doc.can && doc.can.restore) {
          btns.push(el('button', { class: 'btn', type: 'button', text: 'Restore', onclick: function (e) {
            e.stopPropagation();
            ask('Restore version', 'Restore "' + v.name + '"? The current state stays in history and can be reverted.', 'Restore').then(function (ok) {
              if (!ok) return;
              api('POST', '/documents/' + id + '/versions/' + v.id + '/restore', {}).then(function () { toast('info', 'Version restored'); }).catch(function (e) { toast('error', e.message); });
            });
          } }));
        }
        var card = el('div', { class: 'item', onclick: function () { openVersionPreview(id, v); } }, [
          el('div', { class: 'row' }, [
            el('span', { class: 'hist-actor grow', text: v.name }),
            el('span', { class: 'hist-seq', text: '#' + v.seq }),
          ]),
          el('div', { class: 'row' }, [
            el('small', { class: 'grow', text: 'by ' + v.createdBy + ' · ' + ago(v.createdAt) }),
            el('div', { class: 'actions' }, btns),
          ]),
        ]);
        body.appendChild(card);
      });
    }).catch(function (e) { body.textContent = e.message; });
  }
  function renderComments(id, body) {
    body.textContent = 'Loading…';
    api('GET', '/documents/' + id + '/comments').then(function (r) {
      body.textContent = '';
      (r.comments || []).forEach(function (c) {
        body.appendChild(itemRow(c.body, (c.authorType === 'ai' ? '🤖 ' : '') + c.author + ' · ' + ago(c.createdAt) + (c.cell ? ' · cell ' + c.cell : ''), []));
      });
      if (doc.can && doc.can.comment) {
        var input = el('textarea', { class: 'field', rows: '3', maxlength: '4000', placeholder: 'Write a comment', 'aria-label': 'Comment' });
        body.appendChild(input);
        body.appendChild(el('button', { class: 'btn primary', type: 'button', id: 'post-comment', text: 'Post', onclick: function () {
          var text = input.value.trim(); if (!text) return;
          var page = '';
          try { var st = adapter && adapter.confirmed(); if (st && st.pages.size) page = Array.from(st.pages.keys())[0]; } catch (e) { /* none */ }
          api('POST', '/documents/' + id + '/comments', { page: page, cell: '', body: text }).then(function () { renderComments(id, body); }).catch(function (e) { toast('error', e.message); });
        } }));
      }
    }).catch(function (e) { body.textContent = e.message; });
  }

  // ---- share & AI collaborators ----------------------------------------------------------------------------------------
  function makeRoleDropdown(initialValue, onChange, props) {
    var val = initialValue === 'editor' ? 'editor' : 'viewer';
    var root = el('div', Object.assign({ class: 'role-dd' }, props || {}), []);
    var labelSpan = el('span', { text: val });
    var chev = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    chev.setAttribute('viewBox', '0 0 24 24');
    chev.setAttribute('class', 'role-dd-chev');
    chev.setAttribute('fill', 'none');
    chev.setAttribute('stroke', 'currentColor');
    chev.setAttribute('stroke-width', '2.4');
    chev.setAttribute('stroke-linecap', 'round');
    chev.setAttribute('stroke-linejoin', 'round');
    var path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    path.setAttribute('d', 'M6 9l6 6 6-6');
    chev.appendChild(path);
    var btn = el('button', { class: 'role-dd-btn', type: 'button', 'aria-haspopup': 'listbox', 'aria-expanded': 'false' }, [labelSpan, chev]);
    var menuEl = el('div', { class: 'role-dd-menu', role: 'listbox', hidden: true }, []);
    function renderOpts() {
      menuEl.textContent = '';
      ['viewer', 'editor'].forEach(function (opt) {
        var item = el('button', {
          class: 'role-dd-opt' + (opt === val ? ' active' : ''),
          type: 'button',
          role: 'option',
          'data-value': opt,
          onclick: function (e) {
            e.stopPropagation();
            val = opt;
            labelSpan.textContent = val;
            root.dataset.value = val;
            closeDd();
            renderOpts();
            if (onChange) onChange(val);
          }
        }, [el('span', { text: opt }), el('span', { class: 'role-dd-check', text: '✓' })]);
        menuEl.appendChild(item);
      });
    }
    function closeDd() {
      root.classList.remove('open');
      menuEl.hidden = true;
      btn.setAttribute('aria-expanded', 'false');
    }
    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      var wasOpen = !menuEl.hidden;
      Array.prototype.slice.call(document.querySelectorAll('.role-dd.open')).forEach(function (d) {
        d.classList.remove('open');
        var m = d.querySelector('.role-dd-menu');
        if (m) m.hidden = true;
      });
      if (!wasOpen) {
        root.classList.add('open');
        menuEl.hidden = false;
        btn.setAttribute('aria-expanded', 'true');
      }
    });
    renderOpts();
    root.dataset.value = val;
    root.getValue = function () { return val; };
    root.appendChild(btn);
    root.appendChild(menuEl);
    return root;
  }

  document.addEventListener('click', function () {
    Array.prototype.slice.call(document.querySelectorAll('.role-dd.open')).forEach(function (d) {
      d.classList.remove('open');
      var m = d.querySelector('.role-dd-menu');
      if (m) m.hidden = true;
    });
  });

  function helpTip(explanation) {
    var svgNS = 'http://www.w3.org/2000/svg';
    var icon = document.createElementNS(svgNS, 'svg');
    icon.setAttribute('viewBox', '0 0 24 24');
    icon.setAttribute('width', '13');
    icon.setAttribute('height', '13');
    icon.setAttribute('fill', 'none');
    icon.setAttribute('stroke', 'currentColor');
    icon.setAttribute('stroke-width', '2.2');
    icon.setAttribute('stroke-linecap', 'round');
    icon.setAttribute('stroke-linejoin', 'round');
    icon.setAttribute('aria-hidden', 'true');
    var p1 = document.createElementNS(svgNS, 'path');
    p1.setAttribute('d', 'M12 16v-5');
    var p2 = document.createElementNS(svgNS, 'path');
    p2.setAttribute('d', 'M12 7.5h.01');
    icon.appendChild(p1);
    icon.appendChild(p2);
    return el('span', { class: 'help-tip', tabindex: '0', role: 'note', 'aria-label': explanation }, [
      icon,
      el('span', { class: 'help-tip-popup', text: explanation }),
    ]);
  }

  function openShare(id) {
    api('GET', '/documents/' + id + '/permissions').then(function (r) {
      var wrap = el('div', null, []);
      var list = el('div', { id: 'share-list', class: 'share-list' }, []);
      var hits = el('div', { class: 'hits' }, []);
      var q = el('input', { class: 'field', id: 'share-q', type: 'search', placeholder: 'Add people by name or username', autocomplete: 'off', 'aria-label': 'Add people' });
      var roleDd = makeRoleDropdown('editor', null, { id: 'share-role', 'aria-label': 'Role' });
      var addBox = el('div', { id: 'share-add-box', class: 'share-add-box', hidden: true }, []);
      var addTrigger = el('div', { class: 'share-add-trigger' }, []);
      var addBtn = el('button', {
        id: 'btn-add-user',
        class: 'add-user-btn',
        type: 'button',
        onclick: function () {
          addBox.hidden = !addBox.hidden;
          if (!addBox.hidden) setTimeout(function () { q.focus(); }, 20);
        }
      }, [el('span', { class: 'add-user-plus', 'aria-hidden': 'true', text: '+' }), el('span', { text: 'Add user' })]);
      addTrigger.appendChild(addBtn);

      function refresh() {
        api('GET', '/documents/' + id + '/permissions').then(function (x) { r = x; draw(); });
      }
      function setRole(subject, role) {
        api('PUT', '/documents/' + id + '/permissions', { subject: subject, role: role }).then(refresh).catch(function (e) { toast('error', e.message); });
      }
      function draw() {
        list.textContent = '';
        list.appendChild(el('div', { class: 'share-row' }, [el('span', { class: 'grow', text: r.owner }), el('span', { class: 'chip', text: 'owner' })]));
        (r.permissions || []).forEach(function (p) {
          var dd = makeRoleDropdown(p.role, function (newRole) { setRole(p.subject, newRole); }, { 'aria-label': 'Role of ' + p.subject, 'data-subject': p.subject });
          list.appendChild(el('div', { class: 'share-row' }, [
            el('span', { class: 'grow', text: p.subject === '*' ? 'Everyone with access to DrawSpace' : p.subject }),
            dd,
            el('button', { class: 'btn danger', type: 'button', text: 'Remove', 'data-remove': p.subject, onclick: function () { setRole(p.subject, ''); } })
          ]));
        });
      }
      draw();
      var timer = null;
      q.oninput = function () {
        clearTimeout(timer);
        timer = setTimeout(function () {
          var v = q.value.trim(); hits.textContent = '';
          if (v.length < 1) return;
          api('GET', '/users?q=' + encodeURIComponent(v)).then(function (u) {
            (u.users || []).filter(function (x) { return x.username !== r.owner; }).forEach(function (x) {
              hits.appendChild(el('button', { class: 'hit', type: 'button', 'data-user': x.username, text: (x.name ? x.name + ' · ' : '') + x.username + (x.kind === 'ai' ? ' (AI)' : ''),
                onclick: function () { setRole(x.username, roleDd.getValue()); q.value = ''; hits.textContent = ''; addBox.hidden = true; } }));
            });
          }).catch(function () { /* ignore */ });
        }, 200);
      };
      var everyone = el('button', { class: 'btn', type: 'button', id: 'share-everyone', text: 'Share with everyone', onclick: function () { setRole('*', roleDd.getValue()); addBox.hidden = true; } });
      addBox.appendChild(el('div', { class: 'share-add-row' }, [q, roleDd]));
      addBox.appendChild(hits);
      addBox.appendChild(everyone);

      wrap.appendChild(el('label', null, ['People with access']));
      wrap.appendChild(list);
      wrap.appendChild(addTrigger);
      wrap.appendChild(addBox);
      q.addEventListener('keydown', function (e) { if (e.key === 'Enter') e.preventDefault(); }); // Enter in the search box must not close the dialog
      var actions = [{ label: 'Done', cls: 'primary' }];
      dialog('Share "' + (doc ? doc.document.title : '') + '"', wrap, actions);
    }).catch(function (e) { toast('error', e.message); });
  }

  function openAiSettings(id) {
    api('GET', '/documents/' + id + '/permissions').then(function (r) {
      var ai = r.aiPolicy || { allowed: false, maxOpsPerMin: 120, maxDelete: 20 };
      var allowed = el('input', { type: 'checkbox', id: 'ai-allowed' }); allowed.checked = !!ai.allowed;
      var rate = el('input', { class: 'field', id: 'ai-rate', type: 'number', min: '1', max: '5000', value: String(ai.maxOpsPerMin) }); rate.value = String(ai.maxOpsPerMin);
      var del = el('input', { class: 'field', id: 'ai-del', type: 'number', min: '0', max: '1000', value: String(ai.maxDelete) }); del.value = String(ai.maxDelete);
      var canPolicy = doc && doc.can && doc.can.policy;
      [allowed, rate, del].forEach(function (x) { x.disabled = !canPolicy; });
      var aiBox = el('div', { class: 'ai-box' }, [
        el('div', { class: 'ai-setting' }, [
          el('div', { class: 'ai-label-row' }, [
            el('label', { class: 'check' }, [allowed, ' Allow AI collaborators (agents) on this diagram']),
            helpTip('Izinkan agen AI (misal Claude / MCP) yang telah diberi akses pada menu Share untuk membaca dan mengedit diagram ini secara langsung.'),
          ]),
        ]),
        el('div', { class: 'ai-setting' }, [
          el('div', { class: 'ai-label-row' }, [
            el('span', { text: 'Max AI operations per minute' }),
            helpTip('Batas maksimum jumlah operasi perubahan sel/bentuk yang boleh dikirim oleh agen AI setiap menit (rate limit) agar tidak membebani diagram.'),
          ]),
          rate,
        ]),
        el('div', { class: 'ai-setting' }, [
          el('div', { class: 'ai-label-row' }, [
            el('span', { text: 'Max AI deletions per change (larger needs confirmation)' }),
            helpTip('Batas maksimum jumlah elemen/sel yang boleh dihapus oleh agen AI dalam satu kali perintah perubahan guna mencegah penghapusan massal tanpa sengaja.'),
          ]),
          del,
        ]),
      ]);
      if (canPolicy) {
        var saveAi = el('button', { class: 'btn', type: 'button', id: 'save-ai', text: 'Save AI settings', disabled: true, onclick: function () {
          api('PUT', '/documents/' + id + '/ai-policy', { allowed: allowed.checked, maxOpsPerMin: parseInt(rate.value, 10), maxDelete: parseInt(del.value, 10) })
            .then(function () { toast('info', 'AI settings saved'); saveAi.disabled = true; }).catch(function (e) { toast('error', e.message); });
        } });
        [allowed, rate, del].forEach(function (x) { x.addEventListener('input', function () { saveAi.disabled = false; }); x.addEventListener('change', function () { saveAi.disabled = false; }); });
        aiBox.appendChild(saveAi);
      } else {
        aiBox.appendChild(el('small', { text: 'Only the owner or an administrator can change these settings.' }));
      }
      dialog('AI Collaborators — "' + (doc ? doc.document.title : '') + '"', aiBox, [{ label: 'Done', cls: 'primary' }]);
    }).catch(function (e) { toast('error', e.message); });
  }

  // ---- users (local accounts, administrators only) ------------------------------------------------------------------------------------------------------
  function showUsers() {
    $('users-view').hidden = false; document.title = 'Users · DrawSpace'; setStatus('auth', 'Signed in');
    if (!(me.roles || []).includes('admin')) { $('users-body').textContent = ''; toast('error', 'Administrators only'); return; }
    loadUsers();
    $('new-user').onclick = function () { userDialog(null); };
  }
  function loadUsers() {
    api('GET', '/admin/local-users').then(function (r) {
      var body = $('users-body'); body.textContent = '';
      (r.users || []).forEach(function (u) {
        var role = el('select', { class: 'field select', 'aria-label': 'Role of ' + u.username, 'data-user': u.username }, ['user', 'admin'].map(function (x) { return el('option', { value: x, text: x }); }));
        role.value = u.role;
        role.onchange = function () { patchUser(u.username, { role: role.value }); };
        var enabled = el('span', { class: 'chip ' + (u.disabled ? 'off' : 'on'), text: u.disabled ? 'disabled' : 'active' });
        body.appendChild(el('tr', { 'data-user': u.username }, [
          el('td', { text: u.username }), el('td', { text: u.name }), el('td', null, [role]), el('td', null, [enabled]),
          el('td', { text: u.lastLogin ? ago(u.lastLogin) : 'never' }),
          el('td', null, [el('div', { class: 'actions' }, [
            el('button', { class: 'btn', type: 'button', text: 'Edit', onclick: function () { userDialog(u); } }),
            el('button', { class: 'btn', type: 'button', text: u.disabled ? 'Enable' : 'Disable', onclick: function () { patchUser(u.username, { disabled: !u.disabled }); } }),
            el('button', { class: 'btn danger', type: 'button', text: 'Delete', onclick: function () {
              ask('Delete ' + u.username, 'Delete this local account? Its diagrams must be deleted or it cannot be removed (disable it instead).', 'Delete').then(function (ok) {
                if (ok) api('DELETE', '/admin/local-users/' + encodeURIComponent(u.username)).then(loadUsers).catch(function (e) { toast('error', e.message); });
              });
            } }),
          ])]),
        ]));
      });
    }).catch(function (e) { toast('error', e.message); });
  }
  function patchUser(name, body) { api('PATCH', '/admin/local-users/' + encodeURIComponent(name), body).then(loadUsers).catch(function (e) { toast('error', e.message); loadUsers(); }); }
  function randomPassword() {
    var a = new Uint8Array(15); crypto.getRandomValues(a);
    return Array.from(a).map(function (b) { return 'abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789'[b % 57]; }).join('');
  }
  function userDialog(u) {
    var f = { username: el('input', { class: 'field', id: 'nu-username', autocomplete: 'off', spellcheck: 'false', placeholder: 'e.g. budi', maxlength: '32' }),
      name: el('input', { class: 'field', id: 'nu-name', autocomplete: 'off', maxlength: '80' }),
      email: el('input', { class: 'field', id: 'nu-email', type: 'email', autocomplete: 'off', maxlength: '120' }),
      role: el('select', { class: 'field select', id: 'nu-role' }, ['user', 'admin'].map(function (x) { return el('option', { value: x, text: x }); })),
      pw: el('input', { class: 'field', id: 'nu-password', type: 'text', autocomplete: 'new-password', spellcheck: 'false', placeholder: u ? 'leave empty to keep' : 'at least 10 characters' }) };
    if (u) { f.username.value = u.username; f.username.disabled = true; f.name.value = u.name; f.email.value = u.email || ''; f.role.value = u.role; }
    var msg = el('div', { class: 'msg', hidden: true });
    var gen = el('button', { class: 'btn', type: 'button', text: 'Generate', onclick: function () { f.pw.value = randomPassword(); } });
    var body = el('div', { class: 'dlg-body' }, [msg, el('label', null, ['Username', f.username]), el('label', null, ['Display name', f.name]), el('label', null, ['Email (optional)', f.email]), el('label', null, ['Role', f.role]),
      el('label', null, [u ? 'New password' : 'Initial password', el('div', { class: 'pwrow' }, [f.pw, gen])]),
      el('small', { text: 'The user can change it after signing in. Share it through a safe channel.' })]);
    dialog(u ? 'Edit ' + u.username : 'New local user', body, [{ label: 'Cancel' }, { label: u ? 'Save' : 'Create user', cls: 'primary', keep: true, fn: function () {
      var req = u ? api('PATCH', '/admin/local-users/' + encodeURIComponent(u.username), { name: f.name.value, email: f.email.value, role: f.role.value, password: f.pw.value || undefined })
        : api('POST', '/admin/local-users', { username: f.username.value, name: f.name.value, email: f.email.value, role: f.role.value, password: f.pw.value });
      req.then(function () { $('dlg').close(); loadUsers(); toast('info', u ? 'User updated' : 'User created'); }).catch(function (e) { msg.textContent = e.message; msg.hidden = false; });
      return false;
    } }]);
    setTimeout(function () { (u ? f.name : f.username).focus(); }, 30);
  }
  function passwordDialog() {
    var cur = el('input', { class: 'field', id: 'pw-current', type: 'password', autocomplete: 'current-password' });
    var nw = el('input', { class: 'field', id: 'pw-new', type: 'password', autocomplete: 'new-password' });
    var rep = el('input', { class: 'field', id: 'pw-repeat', type: 'password', autocomplete: 'new-password' });
    var msg = el('div', { class: 'msg', hidden: true });
    dialog('Change password', el('div', { class: 'dlg-body' }, [msg, el('label', null, ['Current password', cur]), el('label', null, ['New password (at least 10 characters)', nw]), el('label', null, ['Repeat new password', rep])]),
      [{ label: 'Cancel' }, { label: 'Change password', cls: 'primary', keep: true, fn: function () {
        if (nw.value !== rep.value) { msg.textContent = 'The new passwords do not match.'; msg.hidden = false; return false; }
        api('POST', '/me/password', { current: cur.value, new: nw.value }).then(function () { $('dlg').close(); toast('info', 'Password changed'); }).catch(function (e) { msg.textContent = e.message; msg.hidden = false; });
        return false;
      } }]);
    setTimeout(function () { cur.focus(); }, 30);
  }

  // ---- boot ------------------------------------------------------------------------------------------------------------
  var menu = $('menu'), userbtn = $('userbtn');
  function closeMenu() { menu.hidden = true; userbtn.setAttribute('aria-expanded', 'false'); }
  userbtn.addEventListener('click', function (e) { e.stopPropagation(); var open = menu.hidden; menu.hidden = !open; userbtn.setAttribute('aria-expanded', String(open)); if (open) { var f = menu.querySelector('.menu-item:not([hidden])'); if (f) f.focus(); } });
  document.addEventListener('mousedown', function (e) { if (!menu.hidden && !$('user').contains(e.target)) closeMenu(); });
  window.addEventListener('blur', function () { if (!menu.hidden) closeMenu(); });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !menu.hidden) { closeMenu(); userbtn.focus(); } });
  if ($('theme-day')) $('theme-day').addEventListener('click', function (e) { e.stopPropagation(); applyHostTheme('light', true); });
  if ($('theme-night')) $('theme-night').addEventListener('click', function (e) { e.stopPropagation(); applyHostTheme('dark', true); });
  $('mi-password').addEventListener('click', function () { closeMenu(); passwordDialog(); });

  fetch('/api/me', { credentials: 'same-origin' })
    .then(function (r) { if (!r.ok) throw new Error('unauthenticated'); return r.json(); })
    .then(function (m) {
      me = m; csrf = m.csrf;
      var name = m.name || m.username || '', isAdmin = Array.isArray(m.roles) && m.roles.indexOf('admin') >= 0;
      $('who').textContent = name; $('avatar').textContent = initials(name);
      $('menu-name').textContent = name;
      $('menu-sub').textContent = (m.username || '') + ' · ' + (m.idp === 'local' ? 'local account' : 'SSO') + (isAdmin ? ' · administrator' : '');
      if (isAdmin) { $('role').hidden = false; $('mi-users').hidden = false; }
      if (m.idp === 'local') $('mi-password').hidden = false;
      $('user').hidden = false;
      var match = /^\/d\/([^/]+)\/?$/.exec(location.pathname);
      if (match) showEditor(decodeURIComponent(match[1]));
      else if (location.pathname === '/admin/users') showUsers();
      else showList();
    })
    .catch(function () { location.href = '/login?return_to=' + encodeURIComponent(location.pathname); });

  $('logout').addEventListener('click', function () {
    guardedNavigate(function () {
      var btn = $('logout');
      if (btn.getAttribute('aria-busy') === 'true') return;
      btn.setAttribute('aria-busy', 'true');
      $('logout-label').textContent = 'Signing out…';
      if (adapter) adapter.close();
      fetch('/logout', { method: 'POST', credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf } })
        .then(function (r) { if (!r.ok) throw new Error('logout failed'); return r.json(); })
        .then(function (j) { location.href = j.redirect; })
        .catch(function () {
          btn.removeAttribute('aria-busy');
          $('logout-label').textContent = 'Sign out';
          setStatus('error', 'Sign-out failed, try again');
        });
    });
  });
})();
