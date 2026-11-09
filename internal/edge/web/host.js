// Host application: document list, editor shell (share, history, versions, comments, export, presence).
// Rendering uses textContent / DOM builders only (the CSP forbids inline script and style attributes).
(function () {
  'use strict';
  var $ = function (id) { return document.getElementById(id); };
  var statusEl = $('status'), csrf = '', me = null, adapter = null, doc = null, editorUi = null;
  // Features that exist in the code and the API but are switched off in the UI for now (comments: not useful yet; flip to re-enable).
  var FEATURES = { comments: false };
  var THEME_KEY = 'drawspace_theme';
  var hostThemeTimer = null;

  // Host-side CSS injected into the draw.io iframe (the draw.io files themselves are untouched): theme transitions, a menu bar that follows the DrawSpace theme
  // (draw.io atlas paints it blue in day and black in night), and no GitHub link in the bottom-right corner.
  function editorFrameCss(m) {
    var css = '.geEditor, .geMenubarContainer, .geSidebarContainer, .geDiagramContainer, .geBackgroundPage, .geHdiv, .geVdiv, .geFormatContainer { transition: background-color 320ms ease-in-out, border-color 320ms ease-in-out, color 320ms ease-in-out !important; }'
      + '.geTabContainer a:has(> img[src*="github-logo"]) { display: none !important; }';
    var bar = m === 'dark'
      ? { bg: '#1e293b', border: '#334155', text: '#e2e8f0', accent: '#60a5fa' }
      : { bg: '#ffffff', border: '#e2e8f0', text: '#1e293b', accent: '#1868db' };
    css += '.geMenubarContainer { background-color: ' + bar.bg + ' !important; border-bottom: 1px solid ' + bar.border + ' !important; }'
      + '.geMenubarContainer .geItem, .geMenubarContainer .geItem:visited, .geMenubarContainer .geStatus, .geMenubarContainer .geStatus * { color: ' + bar.text + ' !important; border-radius: 6px; }'
      + '.geMenubarContainer .geItem, .geMenubarContainer .geItem:hover, .geMenubarContainer .geItem:focus, .geMenubarContainer .geItem:active, .geMenubarContainer .geItem.geActiveItem { background: none !important; background-color: transparent !important; box-shadow: none !important; outline: none !important; }'
      // draw.io's atlas theme paints hovered items with `.geAtlas .geMenubarContainer .geItem:hover:not(.geDarkMode *) { background-color: ... !important }`; repeat classes to out-rank it
      + 'body.geAtlas .geMenubarContainer .geItem.geItem.geItem:hover:not(.geDarkMode *), body.geAtlas .geMenubarContainer .geItem.geItem.geItem:active:not(.geDarkMode *) { background: none !important; background-color: transparent !important; }'
      + '.geMenubarContainer .geItem:hover, .geMenubarContainer .geItem:active, .geMenubarContainer .geItem.geActiveItem { color: ' + bar.accent + ' !important; }';
    return css;
  }

  function applyHostTheme(mode, save) {
    var m = mode === 'dark' ? 'dark' : 'light';
    var root = document.documentElement;
    if (save) {
      clearTimeout(hostThemeTimer);
      root.classList.add('theme-transition');
      hostThemeTimer = setTimeout(function () {
        root.classList.remove('theme-transition');
      }, 400);
    }
    root.setAttribute('data-theme', m);
    if (document.body) document.body.setAttribute('data-theme', m);
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
    if (editorUi) {
      try {
        var f = $('editor');
        if (f && f.contentWindow && f.contentWindow.document) {
          var idoc = f.contentWindow.document;
          var style = idoc.getElementById('drawspace-theme-style');
          if (!style) {
            style = idoc.createElement('style');
            style.id = 'drawspace-theme-style';
            idoc.head.appendChild(style);
          }
          style.textContent = editorFrameCss(m);
        }
      } catch (e) { /* cross-origin / not ready */ }
      if (typeof editorUi.setDarkMode === 'function') {
        try { editorUi.setDarkMode(m === 'dark'); } catch (e) { /* ignore */ }
      }
    }
  }
  (function initTheme() {
    var saved = 'light';
    try { saved = localStorage.getItem(THEME_KEY) || 'light'; } catch (e) { /* ignore */ }
    applyHostTheme(saved === 'dark' ? 'dark' : 'light', false);
  })();

  function setStatus(state, text) { statusEl.hidden = state === 'auth'; statusEl.dataset.state = state; $('status-text').textContent = text; }
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
  var allDocs = [], trashDocs = [], loadedTrash = false;
  // the filter lives in the URL (?scope=trash) so a reload or a shared link keeps the view
  function scopeFromUrl() { var s = new URLSearchParams(location.search).get('scope'); return ['all', 'mine', 'shared', 'trash'].indexOf(s) >= 0 ? s : 'all'; }
  var folders = [];
  function loadDocs() {
    var trash = $('scope').value === 'trash';
    var reqs = trash ? [api('GET', '/trash')] : [api('GET', '/documents'), api('GET', '/folders')];
    return Promise.all(reqs).then(function (rs) {
      if (trash) trashDocs = rs[0].documents || []; else { allDocs = rs[0].documents || []; folders = rs[1].folders || []; }
      loadedTrash = trash;
      renderList();
    }).catch(function (e) { setStatus('error', 'Could not load diagrams'); toast('error', e.message); });
  }
  // moves one diagram to the Trash after a confirmation dialog; the Trash is the undo
  // the application admin may move any diagram to the Trash and bring it back; everybody else only their own
  function isAppAdmin() { var r = (me && me.roles) || []; return r.indexOf('admin') >= 0 || r.indexOf('drawio-admin') >= 0; }
  function trashDoc(d) {
    return ask('Move to Trash', '“' + d.title + '” leaves your list. You can bring it back from the Trash.', 'Move to Trash').then(function (ok) {
      if (!ok) return false;
      return api('DELETE', '/documents/' + encodeURIComponent(d.id)).then(function () { toast('info', 'Moved to Trash: ' + d.title); return true; })
        .catch(function (e) { toast('error', e.message); return false; });
    });
  }
  function trashIcon() {
    var s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    s.setAttribute('viewBox', '0 0 24 24'); s.setAttribute('width', '16'); s.setAttribute('height', '16'); s.setAttribute('fill', 'none');
    s.setAttribute('stroke', 'currentColor'); s.setAttribute('stroke-width', '2'); s.setAttribute('stroke-linecap', 'round'); s.setAttribute('stroke-linejoin', 'round'); s.setAttribute('aria-hidden', 'true');
    s.innerHTML = '<path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M19 6l-1 14H6L5 6"/><path d="M10 11v6M14 11v6"/>';
    return s;
  }
  function showList() {
    $('list-view').hidden = false;
    setStatus('auth', 'Signed in');
    $('scope').value = scopeFromUrl();
    loadDocs();
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
    $('filter').oninput = renderList;
    $('new-folder').onclick = function () {
      var cur = currentFolder();
      var parent = cur && cur.real ? cur.id : null;
      prompt2('New folder', 'Name', '').then(function (n) {
        if (!n) return;
        api('POST', '/folders', { name: n, parentId: parent }).then(function (r) { folders.push(r.folder); renderList(); })
          .catch(function (e) { toast('error', e.message); });
      });
    };
    window.addEventListener('popstate', function () { $('scope').value = scopeFromUrl(); if ((scopeFromUrl() === 'trash') !== loadedTrash) loadDocs(); else renderList(); });
    $('scope').onchange = function () {
      var u = new URL(location.href);
      if (this.value === 'all') u.searchParams.delete('scope'); else u.searchParams.set('scope', this.value);
      u.searchParams.delete('folder');
      history.replaceState(null, '', u);
      if ((this.value === 'trash') !== loadedTrash) loadDocs(); else renderList();
    };
  }
  // ---- folders (personal view, ADR-0020) -------------------------------------------------------------------------------
  // Where a diagram sits for ME: 'root', a folder id, 'shared' (the virtual "Shared with me") or 'others' (admin only: not mine, not shared).
  function locOf(d) {
    if (d.placed) {
      if (!d.folderId) return 'root';
      if (folders.some(function (f) { return f.id === d.folderId; })) return d.folderId;
    }
    if (d.owner === me.username) return 'root';
    return d.role ? 'shared' : 'others';
  }
  function folderById(id) { for (var i = 0; i < folders.length; i++) if (folders[i].id === id) return folders[i]; return null; }
  function childrenOf(pid) {
    return folders.filter(function (f) { return (f.parentId || null) === (pid || null); })
      .sort(function (a, b) { return a.name.toLowerCase() < b.name.toLowerCase() ? -1 : 1; });
  }
  // the folder named in the URL: {id, name, real, parentId}; null = the root
  function currentFolder() {
    var id = new URLSearchParams(location.search).get('folder');
    if (id === 'shared') return { id: 'shared', name: 'Shared with me', real: false, parentId: null };
    if (id === 'others' && isAppAdmin()) return { id: 'others', name: 'Other users’ diagrams', real: false, parentId: null };
    var f = id && folderById(id);
    return f ? { id: f.id, name: f.name, real: true, parentId: f.parentId } : null;
  }
  function folderPath(id) { // "A › B" for a real folder id
    var names = [];
    for (var f = folderById(id), n = 0; f && n < 12; f = f.parentId ? folderById(f.parentId) : null, n++) names.unshift(f.name);
    return names.join(' › ');
  }
  function locLabel(l) { return l === 'root' ? 'Diagrams' : l === 'shared' ? 'Shared with me' : l === 'others' ? 'Other users’ diagrams' : folderPath(l); }
  function goFolder(id) {
    var u = new URL(location.href);
    if (id) u.searchParams.set('folder', id); else u.searchParams.delete('folder');
    history.pushState(null, '', u);
    renderList();
  }
  function svgIcon(inner) {
    var s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    s.setAttribute('viewBox', '0 0 24 24'); s.setAttribute('width', '16'); s.setAttribute('height', '16'); s.setAttribute('fill', 'none');
    s.setAttribute('stroke', 'currentColor'); s.setAttribute('stroke-width', '2'); s.setAttribute('stroke-linecap', 'round'); s.setAttribute('stroke-linejoin', 'round'); s.setAttribute('aria-hidden', 'true');
    s.innerHTML = inner;
    return s;
  }
  function folderIcon() { return svgIcon('<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>'); }
  function moveIcon() { return svgIcon('<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M9 13h6M13 10l3 3-3 3"/>'); }
  function pencilIcon() { return svgIcon('<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>'); }

  function canInbox(d) { return d.owner !== me.username && !!d.role; } // the default spot of a diagram shared with me
  function placeDoc(d, target) { // target: folder id | 'root' | 'inbox'
    var body = target === 'inbox' ? { inbox: true } : { folderId: target === 'root' ? null : target };
    return api('PUT', '/documents/' + encodeURIComponent(d.id) + '/placement', body).then(function () {
      d.placed = target !== 'inbox'; d.folderId = (target === 'inbox' || target === 'root') ? null : target;
      toast('info', 'Moved “' + d.title + '” to ' + (target === 'inbox' ? locLabel(d.role ? 'shared' : 'others') : locLabel(target)));
      renderList();
    }).catch(function (e) { toast('error', e.message); });
  }
  // "Move to…": the folder tree as a list of buttons (also the keyboard / touch alternative to dragging)
  function moveDialog(d) {
    var here = locOf(d), own = d.owner === me.username;
    var box = el('div', { class: 'move-tree', role: 'list' });
    function item(label, depth, target, id) {
      var btn = el('button', { class: 'btn ghost move-item' + (here === id ? ' current' : ''), type: 'button', role: 'listitem',
        disabled: here === id, onclick: function () { $('dlg').close(); placeDoc(d, target); } }, [folderIcon(), el('span', { text: label })]);
      btn.style.paddingLeft = (12 + depth * 18) + 'px'; // CSSOM, not an inline style attribute (CSP style-src)
      box.appendChild(btn);
    }
    item('Diagrams (top level)', 0, 'root', 'root');
    (function walk(pid, depth) { childrenOf(pid).forEach(function (f) { item(f.name, depth, f.id, f.id); walk(f.id, depth + 1); }); })(null, 1);
    if (canInbox(d)) item('Shared with me', 0, 'inbox', 'shared');
    else if (!own && !d.role) item('Other users’ diagrams', 0, 'inbox', 'others'); // administrator view of an unshared diagram
    if (!folders.length) box.appendChild(el('p', { class: 'doc-meta', text: 'You have no folders yet. Use “New folder” to create one.' }));
    dialog('Move “' + d.title + '”', box, [{ label: 'Cancel' }]);
  }
  function renameFolder(f) {
    prompt2('Rename folder', 'Name', f.name).then(function (n) {
      if (!n || n === f.name) return;
      api('PATCH', '/folders/' + encodeURIComponent(f.id), { name: n }).then(function (r) { f.name = r.folder.name; renderList(); })
        .catch(function (e) { toast('error', e.message); });
    });
  }
  function deleteFolder(f) {
    var parent = f.parentId ? folderById(f.parentId) : null;
    ask('Delete folder', '“' + f.name + '” is removed. Its diagrams and subfolders move up to ' + (parent ? '“' + parent.name + '”' : 'the top level') + '; nothing is deleted.', 'Delete folder').then(function (ok) {
      if (!ok) return;
      api('DELETE', '/folders/' + encodeURIComponent(f.id)).then(function () {
        if ((currentFolder() || {}).id === f.id) { var u = new URL(location.href); if (f.parentId) u.searchParams.set('folder', f.parentId); else u.searchParams.delete('folder'); history.replaceState(null, '', u); }
        loadDocs();
      }).catch(function (e) { toast('error', e.message); });
    });
  }
  // drag a diagram card onto a folder card / breadcrumb; the drop target decides the placement
  var dragDoc = null;
  function dropTarget(node, target) { // target: folder id | 'root' | 'inbox'; inbox only for diagrams that are not mine
    node.addEventListener('dragover', function (e) {
      if (!dragDoc) return;
      if (target === 'inbox' && !canInbox(dragDoc)) return;
      e.preventDefault(); node.classList.add('drop-over');
    });
    node.addEventListener('dragleave', function () { node.classList.remove('drop-over'); });
    node.addEventListener('drop', function (e) {
      node.classList.remove('drop-over');
      if (!dragDoc) return;
      e.preventDefault();
      var d = dragDoc; dragDoc = null;
      placeDoc(d, target);
    });
  }
  function folderRow(name, meta, id, real, f) {
    var link = el('a', { class: 'doc-title doc-link', href: '?folder=' + encodeURIComponent(id), text: name, onclick: function (e) {
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.button) return;
      e.preventDefault(); goFolder(id);
    } });
    var kids = [el('span', { class: 'folder-ico' }, [folderIcon()]), el('div', { class: 'doc-main' }, [link, el('span', { class: 'doc-meta', text: meta })])];
    if (real) {
      kids.push(el('button', { class: 'btn icon neutral doc-act', type: 'button', title: 'Rename folder', 'aria-label': 'Rename folder ' + name, onclick: function () { renameFolder(f); } }, [pencilIcon()]));
      kids.push(el('button', { class: 'btn icon doc-act', type: 'button', title: 'Delete folder', 'aria-label': 'Delete folder ' + name, onclick: function () { deleteFolder(f); } }, [trashIcon()]));
    }
    var row = el('div', { class: 'doc doc-folder', role: 'listitem' }, kids);
    if (real) dropTarget(row, id); else if (id === 'shared') dropTarget(row, 'inbox');
    return row;
  }
  function docRow(d, loc) {
    var chips = [el('span', { class: 'chip', text: d.role })];
    if (loc) chips.push(el('span', { class: 'chip loc', text: locLabel(loc), title: 'Location' }));
    if (d.aiPolicy && d.aiPolicy.allowed) chips.push(el('span', { class: 'chip ai', text: 'AI allowed' }));
    var n = (d.sharedWith || []).length;
    if (n) chips.push(el('button', { class: 'chip shared', type: 'button', text: 'Shared · ' + n, title: 'Who has access', 'aria-label': 'Shared with ' + n + ' — show who', onclick: function () { showShares(d); } }));
    var kids = [el('div', { class: 'doc-main' }, [
      el('a', { class: 'doc-title doc-link', href: '/d/' + d.id, text: d.title }),
      el('span', { class: 'doc-chips' }, chips),
      el('span', { class: 'doc-meta', text: 'Owner ' + d.owner + ' · updated ' + ago(d.updatedAt) + ' · revision ' + d.headSeq }),
    ])];
    kids.push(el('button', { class: 'btn icon neutral doc-act', type: 'button', title: 'Move to folder…', 'aria-label': 'Move “' + d.title + '” to a folder', onclick: function () { moveDialog(d); } }, [moveIcon()]));
    if (d.owner === me.username || isAppAdmin()) { // the owner (or an application admin) moves a diagram to the Trash
      var del = el('button', { class: 'btn icon doc-act', type: 'button', title: 'Move to Trash', 'aria-label': 'Move “' + d.title + '” to Trash', onclick: function () {
        del.disabled = true;
        trashDoc(d).then(function (done) { if (done) loadDocs(); else del.disabled = false; });
      } }, [trashIcon()]);
      kids.push(del);
    }
    var row = el('div', { class: 'doc', role: 'listitem', draggable: 'true' }, kids);
    row.addEventListener('dragstart', function (e) { dragDoc = d; row.classList.add('dragging'); e.dataTransfer.effectAllowed = 'move'; try { e.dataTransfer.setData('text/plain', d.title); } catch (x) { /* ignore */ } });
    row.addEventListener('dragend', function () { dragDoc = null; row.classList.remove('dragging'); });
    return row;
  }
  function renderCrumbs(cur, box) {
    var nav = $('crumbs');
    nav.textContent = '';
    var parts = [];
    if (cur && cur.real) { for (var f = folderById(cur.id), n = 0; f && n < 12; f = f.parentId ? folderById(f.parentId) : null, n++) parts.unshift(f); }
    function crumb(label, id, last) {
      if (last) return el('span', { class: 'crumb current', 'aria-current': 'page', text: label });
      var a = el('a', { class: 'crumb', href: id ? '?folder=' + encodeURIComponent(id) : location.pathname, text: label, onclick: function (e) {
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.button) return; e.preventDefault(); goFolder(id);
      } });
      dropTarget(a, id || 'root');
      return a;
    }
    var items = [crumb('Diagrams', null, !cur)];
    if (cur && !cur.real) items.push(crumb(cur.name, cur.id, true));
    parts.forEach(function (f, i) { items.push(crumb(f.name, f.id, i === parts.length - 1)); });
    items.forEach(function (c, i) { if (i) nav.appendChild(el('span', { class: 'crumb-sep', 'aria-hidden': 'true', text: '›' })); nav.appendChild(c); });
  }
  function renderList() {
    var q = $('filter').value.trim().toLowerCase(), scope = $('scope').value, box = $('docs');
    box.textContent = '';
    var inTrash = scope === 'trash';
    // folder navigation only in the plain "All" view without a search; a filter or a search lists flat and shows each diagram's location
    var tree = scope === 'all' && !q;
    $('crumbs').hidden = !tree; $('new-folder').hidden = inTrash;
    if (inTrash) { renderTrash(q, box); return; }
    $('empty').textContent = 'No diagrams yet. Create one or import a .drawio file.';
    if (!tree) {
      var flat = allDocs.filter(function (d) {
        if (q && d.title.toLowerCase().indexOf(q) < 0) return false;
        if (scope === 'mine') return d.owner === me.username;
        if (scope === 'shared') return d.owner !== me.username && !!d.role;
        return true;
      });
      flat.forEach(function (d) { box.appendChild(docRow(d, locOf(d))); });
      $('empty').textContent = q ? 'No diagram matches your search.' : 'No diagrams yet. Create one or import a .drawio file.';
      $('empty').hidden = flat.length > 0;
      return;
    }
    var cur = currentFolder(), here = cur ? cur.id : 'root', shown = 0;
    renderCrumbs(cur, box);
    var count = function (id) { return allDocs.filter(function (d) { return locOf(d) === id; }).length + childrenOf(id).length; };
    if (!cur || cur.real) childrenOf(cur ? cur.id : null).forEach(function (f) { box.appendChild(folderRow(f.name, count(f.id) + ' item' + (count(f.id) === 1 ? '' : 's'), f.id, true, f)); shown++; });
    if (!cur) {
      var ns = allDocs.filter(function (d) { return locOf(d) === 'shared'; }).length;
      if (ns) { box.appendChild(folderRow('Shared with me', ns + ' diagram' + (ns === 1 ? '' : 's') + ' shared with you', 'shared', false)); shown++; }
      var no = allDocs.filter(function (d) { return locOf(d) === 'others'; }).length;
      if (no && isAppAdmin()) { box.appendChild(folderRow('Other users’ diagrams', no + ' diagram' + (no === 1 ? '' : 's') + ' (administrator view)', 'others', false)); shown++; }
    }
    allDocs.filter(function (d) { return locOf(d) === here; }).forEach(function (d) { box.appendChild(docRow(d, null)); shown++; });
    $('empty').textContent = cur ? 'This folder is empty. Drag a diagram here or use “Move to folder…”.' : 'No diagrams yet. Create one or import a .drawio file.';
    $('empty').hidden = shown > 0;
  }
  // the people a diagram is shared with, from the list response (owner / admin only)
  function showShares(d) {
    var list = el('ul', { class: 'share-list' }, (d.sharedWith || []).map(function (p) {
      return el('li', null, [el('span', { text: p.subject }), el('span', { class: 'chip', text: p.role })]);
    }));
    dialog('Shared “' + d.title + '”', list, [{ label: 'Close', cls: 'primary' }]);
  }
  // the Trash lists what the caller may bring back (their own diagrams; the application admin sees all)
  function renderTrash(q, box) {
    $('empty').textContent = 'The Trash is empty. Diagrams you delete stay here until you restore them.';
    var rows = trashDocs.filter(function (d) { return !q || d.title.toLowerCase().indexOf(q) >= 0; });
    rows.forEach(function (d) {
      var btn = el('button', { class: 'btn sm doc-act', type: 'button', text: 'Restore Diagram', 'aria-label': 'Restore “' + d.title + '”', onclick: function () {
        btn.disabled = true; btn.textContent = 'Restoring…';
        api('POST', '/documents/' + encodeURIComponent(d.id) + '/undelete').then(function () { toast('info', 'Restored: ' + d.title); loadDocs(); })
          .catch(function (e) { btn.disabled = false; btn.textContent = 'Restore Diagram'; toast('error', e.message); });
      } });
      box.appendChild(el('div', { class: 'doc doc-trash', role: 'listitem' }, [
        el('div', { class: 'doc-main' }, [
          el('span', { class: 'doc-title', text: d.title }),
          el('span', { class: 'doc-chips' }, [el('span', { class: 'chip off', text: 'In Trash' })]),
          el('span', { class: 'doc-meta', text: 'Owner ' + d.owner + ' · deleted ' + ago(d.deletedAt) + ' · revision ' + d.headSeq }),
        ]),
        btn,
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
      win.document.addEventListener('mousedown', function (e) {
        closeMenu();
        if (!(e.target && e.target.closest && e.target.closest('[data-drawspace-menu="ai"]'))) closeAiPop();
      }, true);
      try {
        var idoc = win.document;
        var style = idoc.getElementById('drawspace-theme-style');
        if (!style) {
          style = idoc.createElement('style');
          style.id = 'drawspace-theme-style';
          idoc.head.appendChild(style);
        }
        style.textContent = editorFrameCss(document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light');
      } catch (e) { /* ignore */ }
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
      openAiSettings(id, aiElt);
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
    // File > Move to Trash… for the owner / application admin (the server decides: doc.can.delete); the Trash is the undo
    var fileMenu = ui.menus.get('file');
    if (fileMenu && !fileMenu.drawspaceTrash) {
      var origFile = fileMenu.funct;
      fileMenu.drawspaceTrash = true;
      fileMenu.funct = function (menu, parent) {
        origFile.apply(this, arguments);
        if (doc && doc.can && doc.can['delete']) {
          menu.addSeparator(parent);
          menu.addItem('Move to Trash…', null, function () {
            trashDoc({ id: id, title: (doc.document && doc.document.title) || 'this diagram' }).then(function (done) { if (done) location.href = '/?scope=trash'; });
          }, parent);
        }
      };
    }
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
    badge.hidden = false; badge.dataset.role = role; badge.textContent = role === 'owner' ? 'owner' : edit ? 'can edit' : 'view only';
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
    else if (type === 'deleteCell' || type === 'delCell' || type === 'deletePage' || type === 'delPage') { cls = 'del'; prefix = '− '; }
    else if (type === 'setPos' || type === 'reparent' || type === 'reorder') { cls = 'move'; prefix = '↔ '; }
    else if (type === 'setProp' || type === 'renamePage' || type === 'orderPage' || type === 'setTitle') { cls = 'mod'; prefix = '✎ '; }
    else if (type === 'connect') { cls = 'conn'; prefix = '↗ '; }
    else if (type === 'restoreCells' || type === 'restore') { cls = 'restore'; prefix = '↺ '; }
    return el('span', { class: 'op-pill ' + cls, text: prefix + (type || 'op') });
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
  function cellProp(p, k, fallback) {
    if (!p) return fallback;
    if (p[k] !== undefined && p[k] !== '') return p[k];
    if (p['st:' + k] !== undefined && p['st:' + k] !== '') return p['st:' + k];
    return fallback;
  }
  function cellNum(p, k1, k2, defVal) {
    if (!p) return defVal;
    var v = parseFloat(p[k1]);
    if (!isNaN(v)) return v;
    if (k2) {
      v = parseFloat(p[k2]);
      if (!isNaN(v)) return v;
    }
    return defVal;
  }
  function cellShapeType(c) {
    var p = (c && c.props) || {};
    var st = parseStyleMap(p.style);
    var sn = (p.sn || p['st:shape'] || st.shape || '').toLowerCase();
    if (c && c.k === 'e') return 'edge';
    if (sn === 'umlactor' || st.umlActor === '1') return 'umlActor';
    if (sn === 'ellipse' || st.ellipse === '1') return 'ellipse';
    if (sn === 'rhombus' || st.rhombus === '1') return 'rhombus';
    if (sn === 'swimlane' || st.swimlane === '1') return 'swimlane';
    if (sn === 'flexarrow') return 'flexArrow';
    if (sn === 'cylinder3' || sn === 'cylinder' || st.cylinder === '1') return 'cylinder';
    if (sn === 'text' || st.text === '1' || (p['st:strokeColor'] === 'none' && p['st:fillColor'] === 'none')) return 'text';
    return 'rect';
  }
  function cellDisplayLabel(c) {
    if (!c) return '';
    var p = c.props || {};
    var raw = p.v !== undefined ? p.v : (p['va:label'] !== undefined ? p['va:label'] : (p.value || ''));
    return stripHtml(raw);
  }
  function cellFriendlyName(c, fallbackId) {
    if (!c) return fallbackId || 'Element';
    var st = cellShapeType(c);
    var kindMap = {
      ellipse: 'Ellipse',
      rhombus: 'Rhombus',
      umlActor: 'Actor',
      swimlane: 'Container',
      flexArrow: 'Arrow',
      cylinder: 'Cylinder',
      text: 'Text',
      edge: 'Connector',
      rect: 'Rectangle'
    };
    var kind = kindMap[st] || 'Shape';
    var lbl = cellDisplayLabel(c);
    if (lbl) return kind + ' "' + (lbl.length > 28 ? lbl.slice(0, 27) + '…' : lbl) + '"';
    return kind;
  }
  function buildCellLookup(pages) {
    var map = {};
    (pages || []).forEach(function (pg) {
      var byId = {};
      (pg.cells || []).forEach(function (c) { byId[c.id] = c; });
      function absPos(c, depth) {
        var p = (c && c.props) || {};
        var x = cellNum(p, 'gx', 'x', 0);
        var y = cellNum(p, 'gy', 'y', 0);
        if ((depth || 0) < 6 && c && c.p && c.p !== '0' && c.p !== '1' && byId[c.p]) {
          var par = absPos(byId[c.p], (depth || 0) + 1);
          x += par.x;
          y += par.y;
        }
        return { x: x, y: y };
      }
      (pg.cells || []).forEach(function (c) {
        var p = c.props || {};
        var pos = absPos(c, 0);
        var w = cellNum(p, 'gw', 'w', 120);
        var h = cellNum(p, 'gh', 'h', 60);
        map[c.id] = {
          page: pg.name || pg.id,
          cell: c,
          props: p,
          absX: pos.x,
          absY: pos.y,
          w: w,
          h: h
        };
      });
    });
    return map;
  }
  function drawShapeSvgElement(svgNS, v, fill, stroke, strokeW, dashed, isGhost) {
    var g = document.createElementNS(svgNS, 'g');
    if (isGhost) g.setAttribute('opacity', '0.55');
    var st = v.shapeType;
    var dashAttr = dashed ? '5,4' : '';

    if (st === 'ellipse') {
      var elp = document.createElementNS(svgNS, 'ellipse');
      elp.setAttribute('cx', String(v.x + v.w / 2));
      elp.setAttribute('cy', String(v.y + v.h / 2));
      elp.setAttribute('rx', String(Math.max(2, v.w / 2)));
      elp.setAttribute('ry', String(Math.max(2, v.h / 2)));
      elp.setAttribute('fill', fill);
      elp.setAttribute('stroke', stroke);
      elp.setAttribute('stroke-width', String(strokeW));
      if (dashAttr) elp.setAttribute('stroke-dasharray', dashAttr);
      g.appendChild(elp);
    } else if (st === 'rhombus') {
      var poly = document.createElementNS(svgNS, 'polygon');
      var cx = v.x + v.w / 2, cy = v.y + v.h / 2;
      poly.setAttribute('points', cx + ',' + v.y + ' ' + (v.x + v.w) + ',' + cy + ' ' + cx + ',' + (v.y + v.h) + ' ' + v.x + ',' + cy);
      poly.setAttribute('fill', fill);
      poly.setAttribute('stroke', stroke);
      poly.setAttribute('stroke-width', String(strokeW));
      if (dashAttr) poly.setAttribute('stroke-dasharray', dashAttr);
      g.appendChild(poly);
    } else if (st === 'umlActor') {
      var acx = v.x + v.w / 2;
      var headR = Math.max(4, Math.min(v.w * 0.28, v.h * 0.14));
      var headY = v.y + headR + 2;
      var neckY = headY + headR;
      var armY = v.y + v.h * 0.44;
      var hipY = v.y + v.h * 0.68;
      var footY = v.y + v.h - 2;
      var head = document.createElementNS(svgNS, 'circle');
      head.setAttribute('cx', String(acx));
      head.setAttribute('cy', String(headY));
      head.setAttribute('r', String(headR));
      head.setAttribute('fill', fill);
      head.setAttribute('stroke', stroke);
      head.setAttribute('stroke-width', String(strokeW));
      if (dashAttr) head.setAttribute('stroke-dasharray', dashAttr);
      g.appendChild(head);
      var limbs = document.createElementNS(svgNS, 'path');
      limbs.setAttribute('d',
        'M ' + acx + ' ' + neckY + ' L ' + acx + ' ' + hipY +
        ' M ' + (v.x + 2) + ' ' + armY + ' L ' + (v.x + v.w - 2) + ' ' + armY +
        ' M ' + acx + ' ' + hipY + ' L ' + (v.x + 2) + ' ' + footY +
        ' M ' + acx + ' ' + hipY + ' L ' + (v.x + v.w - 2) + ' ' + footY
      );
      limbs.setAttribute('fill', 'none');
      limbs.setAttribute('stroke', stroke);
      limbs.setAttribute('stroke-width', String(strokeW));
      limbs.setAttribute('stroke-linecap', 'round');
      if (dashAttr) limbs.setAttribute('stroke-dasharray', dashAttr);
      g.appendChild(limbs);
    } else if (st === 'swimlane') {
      var startSize = parseFloat(cellProp(v.props, 'startSize', '26')) || 26;
      var box = document.createElementNS(svgNS, 'rect');
      box.setAttribute('x', String(v.x));
      box.setAttribute('y', String(v.y));
      box.setAttribute('width', String(v.w));
      box.setAttribute('height', String(v.h));
      box.setAttribute('rx', '4');
      box.setAttribute('fill', fill);
      box.setAttribute('stroke', stroke);
      box.setAttribute('stroke-width', String(strokeW));
      if (dashAttr) box.setAttribute('stroke-dasharray', dashAttr);
      g.appendChild(box);
      if (v.h > startSize + 4) {
        var hdr = document.createElementNS(svgNS, 'line');
        hdr.setAttribute('x1', String(v.x));
        hdr.setAttribute('y1', String(v.y + startSize));
        hdr.setAttribute('x2', String(v.x + v.w));
        hdr.setAttribute('y2', String(v.y + startSize));
        hdr.setAttribute('stroke', stroke);
        hdr.setAttribute('stroke-width', String(strokeW));
        g.appendChild(hdr);
      }
    } else if (st === 'cylinder') {
      var cylRy = Math.max(3, Math.min(v.h * 0.22, 14));
      var cylRx = Math.max(3, v.w / 2);
      var cylCx = v.x + cylRx;
      var cylBody = document.createElementNS(svgNS, 'path');
      cylBody.setAttribute('d',
        'M ' + v.x + ' ' + (v.y + cylRy) +
        ' L ' + v.x + ' ' + (v.y + v.h - cylRy) +
        ' A ' + cylRx + ' ' + cylRy + ' 0 0 0 ' + (v.x + v.w) + ' ' + (v.y + v.h - cylRy) +
        ' L ' + (v.x + v.w) + ' ' + (v.y + cylRy)
      );
      cylBody.setAttribute('fill', fill);
      cylBody.setAttribute('stroke', stroke);
      cylBody.setAttribute('stroke-width', String(strokeW));
      if (dashAttr) cylBody.setAttribute('stroke-dasharray', dashAttr);
      g.appendChild(cylBody);

      var cylTop = document.createElementNS(svgNS, 'ellipse');
      cylTop.setAttribute('cx', String(cylCx));
      cylTop.setAttribute('cy', String(v.y + cylRy));
      cylTop.setAttribute('rx', String(cylRx));
      cylTop.setAttribute('ry', String(cylRy));
      cylTop.setAttribute('fill', fill);
      cylTop.setAttribute('stroke', stroke);
      cylTop.setAttribute('stroke-width', String(strokeW));
      if (dashAttr) cylTop.setAttribute('stroke-dasharray', dashAttr);
      g.appendChild(cylTop);
    } else if (st === 'text') {
      if (dashed) {
        var tbox = document.createElementNS(svgNS, 'rect');
        tbox.setAttribute('x', String(v.x));
        tbox.setAttribute('y', String(v.y));
        tbox.setAttribute('width', String(v.w));
        tbox.setAttribute('height', String(v.h));
        tbox.setAttribute('fill', 'none');
        tbox.setAttribute('stroke', stroke);
        tbox.setAttribute('stroke-width', '1');
        tbox.setAttribute('stroke-dasharray', '4,3');
        g.appendChild(tbox);
      }
    } else {
      var rect = document.createElementNS(svgNS, 'rect');
      rect.setAttribute('x', String(v.x));
      rect.setAttribute('y', String(v.y));
      rect.setAttribute('width', String(v.w));
      rect.setAttribute('height', String(v.h));
      if (cellProp(v.props, 'rounded', '0') === '1') rect.setAttribute('rx', '8');
      rect.setAttribute('fill', fill);
      rect.setAttribute('stroke', stroke);
      rect.setAttribute('stroke-width', String(strokeW));
      if (dashAttr) rect.setAttribute('stroke-dasharray', dashAttr);
      g.appendChild(rect);
    }

    var label = v.label;
    if (label && !isGhost) {
      var txt = document.createElementNS(svgNS, 'text');
      var tx = v.x + v.w / 2;
      var ty = v.y + v.h / 2 + 4;
      var anchor = 'middle';
      if (st === 'umlActor') {
        ty = v.y + v.h + 13;
      } else if (st === 'cylinder') {
        ty = v.y + v.h / 2 + 7;
      } else if (st === 'swimlane') {
        var hdrH = parseFloat(cellProp(v.props, 'startSize', '26')) || 26;
        ty = v.y + Math.min(hdrH / 2 + 4, v.h / 2 + 4);
      } else if (st === 'text') {
        tx = v.x + 6;
        anchor = 'start';
      }
      txt.setAttribute('x', String(tx));
      txt.setAttribute('y', String(ty));
      txt.setAttribute('text-anchor', anchor);
      txt.setAttribute('font-size', '11.5');
      txt.setAttribute('font-weight', st === 'swimlane' ? '600' : '500');
      txt.setAttribute('font-family', 'Inter, system-ui, sans-serif');
      txt.setAttribute('fill', v.hl === 'deleted' ? '#991b1b' : '#0f172a');
      txt.textContent = label.length > 26 ? label.slice(0, 25) + '…' : label;
      g.appendChild(txt);
    }
    return g;
  }

  function renderDiagramPreviewSvg(pages, highlightMap, deletedCells, moveArrows) {
    var svgNS = 'http://www.w3.org/2000/svg';
    var svg = document.createElementNS(svgNS, 'svg');
    var pg = (pages && pages[0]) || { cells: [] };
    var rawCells = (pg.cells || []).slice();
    (deletedCells || []).forEach(function (dc) {
      if (dc && !rawCells.some(function (c) { return c.id === dc.id; })) rawCells.push(dc);
    });

    var byId = {};
    rawCells.forEach(function (c) { if (c && c.id) byId[c.id] = c; });
    function resolveXY(c, depth) {
      var p = (c && c.props) || {};
      var x = cellNum(p, 'gx', 'x', NaN);
      var y = cellNum(p, 'gy', 'y', NaN);
      if (isNaN(x)) x = 0;
      if (isNaN(y)) y = 0;
      if ((depth || 0) < 6 && c && c.p && c.p !== '0' && c.p !== '1' && byId[c.p]) {
        var par = resolveXY(byId[c.p], (depth || 0) + 1);
        x += par.x;
        y += par.y;
      }
      return { x: x, y: y };
    }

    var vertices = {}, vertexList = [], edges = [];
    var minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    function expandBounds(x, y, w, h) {
      if (!isFinite(x) || !isFinite(y)) return;
      if (x < minX) minX = x;
      if (y < minY) minY = y;
      if (x + w > maxX) maxX = x + w;
      if (y + h > maxY) maxY = y + h;
    }

    rawCells.forEach(function (c) {
      if (!c || c.id === '0' || c.id === '1' || c.k === '-') return;
      var p = c.props || {};
      if (c.k === 'v') {
        var pos = resolveXY(c, 0);
        var w = cellNum(p, 'gw', 'w', 120);
        var h = cellNum(p, 'gh', 'h', 60);
        if (w <= 0) w = 100;
        if (h <= 0) h = 40;
        var st = cellShapeType(c);
        var vObj = {
          id: c.id,
          parent: c.p || '1',
          x: pos.x,
          y: pos.y,
          w: w,
          h: h,
          props: p,
          shapeType: st,
          label: cellDisplayLabel(c),
          hl: highlightMap && highlightMap[c.id]
        };
        vertices[c.id] = vObj;
        vertexList.push(vObj);
        expandBounds(pos.x, pos.y, w, h + (st === 'umlActor' ? 16 : 0));
      } else if (c.k === 'e') {
        edges.push({ id: c.id, cell: c, props: p, parent: c.p || '1' });
      }
    });

    (moveArrows || []).forEach(function (mv) {
      expandBounds(mv.fromX, mv.fromY, mv.fromW, mv.fromH);
      expandBounds(mv.toX, mv.toY, mv.toW, mv.toH);
    });

    // Also include edge floating coordinates in bounds
    edges.forEach(function (e) {
      var p = e.props || {};
      if (p.gex) {
        try {
          var gex = JSON.parse(p.gex);
          if (Array.isArray(gex.sp)) expandBounds(gex.sp[0], gex.sp[1], 4, 4);
          if (Array.isArray(gex.tp)) expandBounds(gex.tp[0], gex.tp[1], 4, 4);
          if (Array.isArray(gex.p)) gex.p.forEach(function (pt) { if (Array.isArray(pt)) expandBounds(pt[0], pt[1], 4, 4); });
        } catch (err) { /* ignore malformed gex */ }
      }
    });

    if (!isFinite(minX)) { minX = 0; minY = 0; maxX = 520; maxY = 240; }
    var pad = 44;
    var vw = Math.max(360, (maxX - minX) + pad * 2);
    var vh = Math.max(200, (maxY - minY) + pad * 2);
    var vx = minX - pad;
    var vy = minY - pad;
    svg.setAttribute('viewBox', vx + ' ' + vy + ' ' + vw + ' ' + vh);
    svg.setAttribute('preserveAspectRatio', 'xMidYMid meet');

    var defs = document.createElementNS(svgNS, 'defs');
    function addMarker(id, color) {
      var m = document.createElementNS(svgNS, 'marker');
      m.setAttribute('id', id);
      m.setAttribute('viewBox', '0 0 10 10');
      m.setAttribute('refX', '8');
      m.setAttribute('refY', '5');
      m.setAttribute('markerWidth', '6');
      m.setAttribute('markerHeight', '6');
      m.setAttribute('orient', 'auto-start-reverse');
      var mp = document.createElementNS(svgNS, 'path');
      mp.setAttribute('d', 'M 0 1 L 10 5 L 0 9 z');
      mp.setAttribute('fill', color);
      m.appendChild(mp);
      defs.appendChild(m);
    }
    addMarker('pv-arrow', '#475569');
    addMarker('pv-arrow-add', '#16a34a');
    addMarker('pv-arrow-mod', '#2563eb');
    addMarker('pv-arrow-del', '#dc2626');
    addMarker('pv-arrow-move', '#7c3aed');

    var gridPat = document.createElementNS(svgNS, 'pattern');
    gridPat.setAttribute('id', 'pv-grid');
    gridPat.setAttribute('width', '20');
    gridPat.setAttribute('height', '20');
    gridPat.setAttribute('patternUnits', 'userSpaceOnUse');
    var gridPath = document.createElementNS(svgNS, 'path');
    gridPath.setAttribute('d', 'M 20 0 L 0 0 0 20');
    gridPath.setAttribute('fill', 'none');
    gridPath.setAttribute('stroke', '#f1f5f9');
    gridPath.setAttribute('stroke-width', '1');
    gridPat.appendChild(gridPath);
    defs.appendChild(gridPat);
    svg.appendChild(defs);

    var bgRect = document.createElementNS(svgNS, 'rect');
    bgRect.setAttribute('x', String(vx));
    bgRect.setAttribute('y', String(vy));
    bgRect.setAttribute('width', String(vw));
    bgRect.setAttribute('height', String(vh));
    bgRect.setAttribute('fill', 'url(#pv-grid)');
    svg.appendChild(bgRect);

    // 1. Draw ghost shapes at previous positions for moved/resized items
    (moveArrows || []).forEach(function (mv) {
      var ghostV = {
        x: mv.fromX,
        y: mv.fromY,
        w: mv.fromW,
        h: mv.fromH,
        props: mv.props || {},
        shapeType: mv.shapeType || 'rect',
        label: ''
      };
      svg.appendChild(drawShapeSvgElement(svgNS, ghostV, 'rgba(124, 58, 237, 0.06)', '#7c3aed', 1.5, true, true));
    });

    // 2. Sort vertices so parent containers draw before child items
    vertexList.sort(function (a, b) {
      var aChild = a.parent && a.parent !== '0' && a.parent !== '1' ? 1 : 0;
      var bChild = b.parent && b.parent !== '0' && b.parent !== '1' ? 1 : 0;
      return aChild - bChild;
    });

    vertexList.forEach(function (v) {
      var st = parseStyleMap(v.props.style);
      var rawFill = cellProp(v.props, 'fillColor', st.fillColor || '#ffffff');
      var rawStroke = cellProp(v.props, 'strokeColor', st.strokeColor || '#334155');
      var fill = rawFill === 'none' ? 'none' : rawFill;
      var stroke = rawStroke === 'none' ? 'none' : rawStroke;
      var hl = v.hl;
      if (hl === 'deleted') {
        fill = '#fee2e2';
        stroke = '#dc2626';
      } else if (hl === 'added') {
        stroke = '#16a34a';
      } else if (hl === 'modified' || hl === 'moved') {
        stroke = '#2563eb';
      }

      var outerG = document.createElementNS(svgNS, 'g');
      if (hl) {
        var ring = document.createElementNS(svgNS, 'rect');
        ring.setAttribute('x', String(v.x - 5));
        ring.setAttribute('y', String(v.y - 5));
        ring.setAttribute('width', String(v.w + 10));
        ring.setAttribute('height', String(v.h + 10));
        ring.setAttribute('rx', '8');
        ring.setAttribute('fill', hl === 'added' ? 'rgba(22, 163, 74, 0.07)' : hl === 'deleted' ? 'rgba(220, 38, 38, 0.08)' : 'rgba(37, 99, 235, 0.07)');
        ring.setAttribute('stroke', hl === 'added' ? '#16a34a' : hl === 'deleted' ? '#dc2626' : '#2563eb');
        ring.setAttribute('stroke-width', '2');
        if (hl === 'deleted') ring.setAttribute('stroke-dasharray', '5,4');
        outerG.appendChild(ring);
      }
      var dashed = hl === 'deleted' || cellProp(v.props, 'dashed', st.dashed || '0') === '1';
      outerG.appendChild(drawShapeSvgElement(svgNS, v, fill, stroke, hl ? 2 : 1.6, dashed, false));
      svg.appendChild(outerG);
    });

    // 3. Draw edges (both connected src/tgt and floating gex endpoints/waypoints)
    edges.forEach(function (e) {
      var p = e.props || {};
      var sId = p.src || p.source;
      var tId = p.tgt || p.target;
      var s = sId ? vertices[sId] : null;
      var t = tId ? vertices[tId] : null;
      var gex = null;
      if (p.gex) {
        try { gex = JSON.parse(p.gex); } catch (err) { gex = null; }
      }
      var x1 = s ? s.x + s.w / 2 : (gex && Array.isArray(gex.sp) ? gex.sp[0] : minX + 40);
      var y1 = s ? s.y + s.h / 2 : (gex && Array.isArray(gex.sp) ? gex.sp[1] : minY + 40);
      var x2 = t ? t.x + t.w / 2 : (gex && Array.isArray(gex.tp) ? gex.tp[0] : x1 + 90);
      var y2 = t ? t.y + t.h / 2 : (gex && Array.isArray(gex.tp) ? gex.tp[1] : y1);

      var pts = [[x1, y1]];
      if (gex && Array.isArray(gex.p)) {
        gex.p.forEach(function (wp) {
          if (Array.isArray(wp) && wp.length >= 2) pts.push([wp[0], wp[1]]);
        });
      }
      pts.push([x2, y2]);

      var hl = highlightMap && highlightMap[e.id];
      var stroke = hl === 'added' ? '#16a34a' : hl === 'deleted' ? '#dc2626' : hl ? '#2563eb' : '#475569';
      var markerId = hl === 'added' ? 'pv-arrow-add' : hl === 'deleted' ? 'pv-arrow-del' : hl ? 'pv-arrow-mod' : 'pv-arrow';
      var poly = document.createElementNS(svgNS, 'polyline');
      poly.setAttribute('points', pts.map(function (pt) { return pt[0] + ',' + pt[1]; }).join(' '));
      poly.setAttribute('fill', 'none');
      poly.setAttribute('stroke', stroke);
      poly.setAttribute('stroke-width', hl ? '2.5' : '1.8');
      if (hl === 'deleted' || cellProp(p, 'dashed', '0') === '1') poly.setAttribute('stroke-dasharray', '5,4');
      if (cellProp(p, 'endArrow', 'classic') !== 'none') {
        poly.setAttribute('marker-end', 'url(#' + markerId + ')');
      }
      svg.appendChild(poly);
    });

    // 4. Draw movement trajectory arrows on top so user clearly sees before -> after motion
    (moveArrows || []).forEach(function (mv) {
      var x1 = mv.fromX + mv.fromW / 2;
      var y1 = mv.fromY + mv.fromH / 2;
      var x2 = mv.toX + mv.toW / 2;
      var y2 = mv.toY + mv.toH / 2;
      if (Math.hypot(x2 - x1, y2 - y1) > 4) {
        var mline = document.createElementNS(svgNS, 'line');
        mline.setAttribute('x1', String(x1));
        mline.setAttribute('y1', String(y1));
        mline.setAttribute('x2', String(x2));
        mline.setAttribute('y2', String(y2));
        mline.setAttribute('stroke', '#7c3aed');
        mline.setAttribute('stroke-width', '2');
        mline.setAttribute('stroke-dasharray', '4,3');
        mline.setAttribute('marker-end', 'url(#pv-arrow-move)');
        svg.appendChild(mline);
      }
    });

    return svg;
  }

  function describeBatchChanges(ops, stateBefore, stateAfter) {
    var beforeMap = buildCellLookup(stateBefore);
    var afterMap = buildCellLookup(stateAfter);
    var highlightMap = {};
    var deletedCells = [];
    var moveArrows = [];
    var rows = [];

    (ops || []).forEach(function (op) {
      var t = op.t || op.op || 'op';
      var cid = op.cell || op.id || op.page || '';
      var bEntry = beforeMap[cid];
      var aEntry = afterMap[cid];
      var bCell = bEntry && bEntry.cell;
      var aCell = aEntry && aEntry.cell;
      var refCell = aCell || bCell || (op.r && op.r.prev && op.r.prev.cells && op.r.prev.cells[0]);
      var friendly = cellFriendlyName(refCell, cid);
      var detailKids = [];

      if (t === 'addCell') {
        highlightMap[cid] = 'added';
        detailKids.push(el('strong', { text: 'Added ' + friendly }));
        if (aEntry && aCell && aCell.k === 'v') {
          detailKids.push(' at ');
          detailKids.push(el('span', { class: 'diff-val-new', text: '(' + Math.round(aEntry.absX) + ', ' + Math.round(aEntry.absY) + ') · ' + Math.round(aEntry.w) + '×' + Math.round(aEntry.h) }));
        }
      } else if (t === 'deleteCell' || t === 'delCell') {
        highlightMap[cid] = 'deleted';
        if (bCell) deletedCells.push(bCell);
        if (op.r && Array.isArray(op.r.del)) {
          op.r.del.forEach(function (delId) {
            highlightMap[delId] = 'deleted';
            if (beforeMap[delId] && beforeMap[delId].cell) deletedCells.push(beforeMap[delId].cell);
          });
        }
        detailKids.push(el('strong', { text: 'Deleted ' + friendly }));
        if (bEntry && bCell && bCell.k === 'v') {
          detailKids.push(' from ');
          detailKids.push(el('span', { class: 'diff-val-old', text: '(' + Math.round(bEntry.absX) + ', ' + Math.round(bEntry.absY) + ')' }));
        }
      } else if (t === 'setPos') {
        if (!highlightMap[cid]) highlightMap[cid] = 'moved';
        var prevX = bEntry ? bEntry.absX : (op.r && op.r.prev && op.r.prev.rx !== undefined ? op.r.prev.rx : 0);
        var prevY = bEntry ? bEntry.absY : (op.r && op.r.prev && op.r.prev.ry !== undefined ? op.r.prev.ry : 0);
        var prevW = bEntry ? bEntry.w : ((op.r && op.r.prev && parseFloat(op.r.prev.gw)) || (aEntry && aEntry.w) || 120);
        var prevH = bEntry ? bEntry.h : ((op.r && op.r.prev && parseFloat(op.r.prev.gh)) || (aEntry && aEntry.h) || 60);
        var newX = aEntry ? aEntry.absX : (op.r && op.r.rx !== undefined ? op.r.rx : prevX);
        var newY = aEntry ? aEntry.absY : (op.r && op.r.ry !== undefined ? op.r.ry : prevY);
        var newW = aEntry ? aEntry.w : (parseFloat(op.gw) || prevW);
        var newH = aEntry ? aEntry.h : (parseFloat(op.gh) || prevH);
        if (refCell && refCell.k === 'v') {
          moveArrows.push({
            id: cid,
            fromX: prevX, fromY: prevY, fromW: prevW, fromH: prevH,
            toX: newX, toY: newY, toW: newW, toH: newH,
            props: refCell.props || {},
            shapeType: cellShapeType(refCell)
          });
        }
        var oldStr = '(' + Math.round(prevX) + ', ' + Math.round(prevY) + ') ' + Math.round(prevW) + '×' + Math.round(prevH);
        var newStr = '(' + Math.round(newX) + ', ' + Math.round(newY) + ') ' + Math.round(newW) + '×' + Math.round(newH);
        detailKids.push(el('strong', { text: 'Moved / resized ' + friendly + ': ' }));
        detailKids.push(el('span', { class: 'diff-val-old', text: oldStr }));
        detailKids.push(' → ');
        detailKids.push(el('span', { class: 'diff-val-new', text: newStr }));
      } else if (t === 'setProp') {
        if (!highlightMap[cid]) highlightMap[cid] = 'modified';
        var rawKey = op.key || 'property';
        var cleanKey = rawKey === 'v' || rawKey === 'va:label' ? 'label' : rawKey.replace(/^st:/, '');
        var oldV = op.r && op.r.prev && op.r.prev.val !== undefined ? stripHtml(op.r.prev.val) : (bEntry && bEntry.props[rawKey] !== undefined ? stripHtml(bEntry.props[rawKey]) : '(empty)');
        var newV = op.val !== undefined && op.val !== null ? stripHtml(op.val) : (aEntry && aEntry.props[rawKey] !== undefined ? stripHtml(aEntry.props[rawKey]) : '(empty)');
        detailKids.push(el('strong', { text: 'Updated ' + friendly + ' · ' + cleanKey + ': ' }));
        detailKids.push(el('span', { class: 'diff-val-old', text: oldV || '(empty)' }));
        detailKids.push(' → ');
        detailKids.push(el('span', { class: 'diff-val-new', text: newV || '(empty)' }));
      } else if (t === 'connect') {
        if (!highlightMap[cid]) highlightMap[cid] = 'modified';
        var term = (op.term || op.end) === 'src' ? 'source' : 'target';
        var tgtCell = op.target ? (afterMap[op.target] || beforeMap[op.target]) : null;
        var tgtName = tgtCell ? cellFriendlyName(tgtCell.cell, op.target) : (op.target || 'floating point');
        detailKids.push(el('strong', { text: 'Connected ' + friendly + ' (' + term + ' → ' + tgtName + ')' }));
      } else if (t === 'restoreCells') {
        var rCells = (op.r && op.r.prev && op.r.prev.cells) || [];
        rCells.forEach(function (rc) { if (rc && rc.id) highlightMap[rc.id] = 'added'; });
        var firstRestored = rCells[0] ? cellFriendlyName(rCells[0], rCells[0].id) : 'elements';
        detailKids.push(el('strong', { text: 'Restored ' + (rCells.length > 1 ? rCells.length + ' elements (' + firstRestored + '…)' : firstRestored) }));
      } else if (t === 'restore') {
        detailKids.push(el('strong', { text: 'Restored full diagram snapshot' }));
      } else {
        detailKids.push(el('strong', { text: t + ' · ' + friendly }));
      }

      rows.push(el('div', { class: 'diff-row' }, [
        opPill(t),
        el('div', { class: 'diff-cell-text' }, detailKids),
      ]));
    });
    return { rows: rows, highlightMap: highlightMap, deletedCells: deletedCells, moveArrows: moveArrows };
  }

  function openHistoryPreview(id, h, body) {
    var who = h.actorType === 'ai' ? '🤖 ' + (h.agent || h.actor) + (h.onBehalfOf ? ' for ' + h.onBehalfOf : '') : h.actor;
    api('GET', '/documents/' + id + '/history/' + encodeURIComponent(h.batch)).then(function (detail) {
      var diff = describeBatchChanges(detail.ops || [], detail.stateBefore || [], detail.stateAfter || []);
      var svgHolder = el('div', { class: 'preview-svg' }, []);
      var captionSpan = el('span', { text: 'Visual diff at revision #' + detail.toSeq + ' (green = added, blue/purple = moved/edited, red dashed = deleted)' });
      var tabDiff = el('button', { class: 'preview-tab active', type: 'button', text: 'Diff (Before → After)' });
      var tabAfter = el('button', { class: 'preview-tab', type: 'button', text: 'After (#' + detail.toSeq + ')' });
      var tabBefore = el('button', { class: 'preview-tab', type: 'button', text: 'Before (#' + Math.max(0, (detail.fromSeq || 1) - 1) + ')' });

      function setPreviewMode(mode) {
        [tabDiff, tabAfter, tabBefore].forEach(function (b) { b.classList.remove('active'); });
        svgHolder.textContent = '';
        if (mode === 'after') {
          tabAfter.classList.add('active');
          captionSpan.textContent = 'Exact diagram state after revision #' + detail.toSeq;
          svgHolder.appendChild(renderDiagramPreviewSvg(detail.stateAfter || [], null, null, null));
        } else if (mode === 'before') {
          tabBefore.classList.add('active');
          captionSpan.textContent = 'Exact diagram state before revision #' + detail.fromSeq;
          svgHolder.appendChild(renderDiagramPreviewSvg(detail.stateBefore || [], null, null, null));
        } else {
          tabDiff.classList.add('active');
          captionSpan.textContent = 'Visual diff at revision #' + detail.toSeq + ' (green = added, blue/purple = moved/edited, red dashed = deleted)';
          svgHolder.appendChild(renderDiagramPreviewSvg(detail.stateAfter || [], diff.highlightMap, diff.deletedCells, diff.moveArrows));
        }
      }
      tabDiff.onclick = function () { setPreviewMode('diff'); };
      tabAfter.onclick = function () { setPreviewMode('after'); };
      tabBefore.onclick = function () { setPreviewMode('before'); };
      setPreviewMode('diff');

      var wrap = el('div', { class: 'preview-wrap' }, [
        el('div', { class: 'preview-meta' }, [
          el('span', null, ['Actor: ', el('strong', { text: who })]),
          el('span', null, ['Revision: ', el('strong', { text: '#' + detail.fromSeq + (detail.toSeq !== detail.fromSeq ? '–#' + detail.toSeq : '') })]),
          el('span', null, ['When: ', el('strong', { text: ago(h.at) })]),
          el('span', null, ['Operations: ', el('strong', { text: String((detail.ops || []).length) })]),
        ]),
        el('div', { class: 'preview-canvas-box' }, [
          el('div', { class: 'preview-canvas-head' }, [
            captionSpan,
            el('div', { class: 'preview-tabs' }, [tabDiff, tabAfter, tabBefore]),
          ]),
          svgHolder,
        ]),
        el('div', { class: 'preview-diff-list' }, diff.rows.length ? diff.rows : [el('small', { text: 'No cell-level details recorded.' })]),
      ]);
      var actions = [{ label: 'Close' }];
      if (doc && doc.can && doc.can.edit) {
        if (detail.headSeq && detail.toSeq < detail.headSeq) {
          actions.push({
            label: 'Restore to #' + detail.toSeq,
            fn: function () {
              api('POST', '/documents/' + id + '/revert', { batch: h.batch, mode: 'restore_to' }).then(function (x) {
                toast('info', 'Restored diagram to revision #' + detail.toSeq + ' (' + x.applied + ' op' + (x.applied === 1 ? '' : 's') + ')');
                if (adapter && typeof adapter.resync === 'function') adapter.resync('restore');
                if (panelRender) panelRender();
              }).catch(function (e) { toast('error', e.message); });
            }
          });
        }
        actions.push({
          label: 'Revert this change',
          cls: 'primary',
          fn: function () {
            api('POST', '/documents/' + id + '/revert', { batch: h.batch }).then(function (x) {
              toast('info', 'Reverted change #' + detail.toSeq + ' (' + x.applied + ' operation' + (x.applied === 1 ? '' : 's') + ')');
              if (adapter && typeof adapter.resync === 'function') adapter.resync('revert');
              if (panelRender) panelRender();
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
      var lookup = buildCellLookup(pages);
      var summaryRows = [];
      pages.forEach(function (pg) {
        (pg.cells || []).forEach(function (c) {
          if (!c || c.id === '0' || c.id === '1' || c.k === '-') return;
          var entry = lookup[c.id];
          var friendly = cellFriendlyName(c, c.id);
          summaryRows.push(el('div', { class: 'diff-row' }, [
            opPill(c.k === 'e' ? 'connect' : 'addCell'),
            el('div', { class: 'diff-cell-text' }, [
              el('strong', { text: friendly }),
              c.k === 'v' && entry ? ' at (' + Math.round(entry.absX) + ', ' + Math.round(entry.absY) + '), ' + Math.round(entry.w) + '×' + Math.round(entry.h) : '',
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
          el('div', { class: 'preview-svg' }, [renderDiagramPreviewSvg(pages, null, null, null)]),
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
              if (adapter && typeof adapter.resync === 'function') adapter.resync('restore');
              if (panelRender) panelRender();
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
          el('button', { class: 'btn sm', type: 'button', text: 'Preview', 'data-preview-batch': h.batch, onclick: function (e) { e.stopPropagation(); openHistoryPreview(id, h, body); } }),
        ];
        if (doc.can && doc.can.edit) {
          btns.push(el('button', { class: 'btn sm', type: 'button', text: 'Revert', 'data-batch': h.batch, onclick: function (e) {
            e.stopPropagation();
            ask('Revert change ' + seqLabel, 'Undo change ' + seqLabel + ' by ' + who + '?', 'Revert').then(function (ok) {
              if (!ok) return;
              api('POST', '/documents/' + id + '/revert', { batch: h.batch }).then(function (x) {
                toast('info', 'Reverted change ' + seqLabel + ' (' + x.applied + ' operation' + (x.applied === 1 ? '' : 's') + ')');
                if (adapter && typeof adapter.resync === 'function') adapter.resync('revert');
                renderHistory(id, body);
              }).catch(function (e) { toast('error', e.message); });
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
              api('POST', '/documents/' + id + '/versions/' + v.id + '/restore', {}).then(function () {
                toast('info', 'Version restored');
                if (adapter && typeof adapter.resync === 'function') adapter.resync('restore');
                if (panelRender) panelRender();
              }).catch(function (e) { toast('error', e.message); });
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

  // ---- MCP access (ADR-0016): per-diagram tokens an AI agent presents to /mcp; opened from the AI menu, owner only ------------------------------------
  function openMcpAccess(id) {
    dialog('MCP access', buildMcpPanel(id), [{ label: 'Done', cls: 'primary' }], { wide: true });
  }
  function buildMcpPanel(id) {
    var sec = el('section', { id: 'mcp-access', class: 'mcp-access' }, []);
    var mcpUrl = location.origin + '/mcp';
    var listBox = el('div', { id: 'mcp-list', class: 'mcp-list' }, []);
    var created = el('div', { id: 'mcp-created', class: 'mcp-created', hidden: true, role: 'status' }, []);
    var nameIn = el('input', { class: 'field mcp-name', id: 'mcp-name', name: 'mcp-token-name', type: 'text', maxlength: '60', placeholder: 'Token name, e.g. Reporting agent…', autocomplete: 'off', spellcheck: 'false', 'aria-label': 'Token name' });
    var roleSel = el('select', { class: 'field mcp-select', id: 'mcp-role', name: 'mcp-token-role', 'aria-label': 'Token access level' }, [
      el('option', { value: 'editor', text: 'Can edit' }), el('option', { value: 'viewer', text: 'Can view' })]);
    var expSel = el('select', { class: 'field mcp-select', id: 'mcp-exp', name: 'mcp-token-expiry', 'aria-label': 'Token expiry' }, [
      el('option', { value: '30', text: 'Expires in 30 days' }), el('option', { value: '90', text: 'Expires in 90 days' }),
      el('option', { value: '365', text: 'Expires in 1 year' }), el('option', { value: '0', text: 'Never expires' })]);
    var dateFmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
    function fmt(t) { return t ? dateFmt.format(new Date(t)) : '—'; }
    function copyBtn(label, text) {
      return el('button', { class: 'btn', type: 'button', text: label, onclick: function (ev) {
        var b = ev.currentTarget;
        navigator.clipboard.writeText(text).then(function () { b.textContent = 'Copied'; setTimeout(function () { b.textContent = label; }, 1500); }, function () { toast('error', 'Copy failed: select the text and copy it manually'); });
      } });
    }
    // a button that asks once, inline, before it calls DELETE (revoking and deleting cannot be undone)
    function dangerBtn(label, busy, attrs, path) {
      return el('button', Object.assign({ class: 'btn danger', type: 'button', text: label, onclick: function (ev) {
        var b = ev.currentTarget;
        if (!b.dataset.armed) {
          b.dataset.armed = '1'; b.textContent = 'Confirm ' + label.toLowerCase();
          setTimeout(function () { delete b.dataset.armed; if (!b.disabled) b.textContent = label; }, 4000);
          return;
        }
        b.disabled = true; b.textContent = busy;
        api('DELETE', path).then(load).catch(function (e) { b.disabled = false; b.textContent = label; delete b.dataset.armed; toast('error', e.message); });
      } }, attrs));
    }
    function drawList(tokens) {
      listBox.textContent = '';
      if (!tokens.length) { listBox.appendChild(el('p', { class: 'mcp-empty', text: 'No tokens yet.' })); return; }
      tokens.forEach(function (t) {
        var dead = t.revokedAt || (t.expiresAt && new Date(t.expiresAt) < new Date());
        var state = t.revokedAt ? 'revoked' : (dead ? 'expired' : t.role);
        listBox.appendChild(el('div', { class: 'share-row mcp-row' + (dead ? ' dead' : ''), 'data-token': t.id }, [
          el('span', { class: 'grow' }, [el('strong', { text: t.name }), el('small', { class: 'mcp-meta', text: ' …' + t.hint + ' · created ' + fmt(t.createdAt) + ' · last used ' + fmt(t.lastUsedAt) + (t.expiresAt ? ' · expires ' + fmt(t.expiresAt) : ' · never expires') })]),
          el('span', { class: 'chip', text: state }),
          dead
            ? dangerBtn('Delete', 'Deleting…', { 'data-delete': t.id }, '/documents/' + id + '/mcp-tokens/' + t.id + '?purge=1')
            : dangerBtn('Revoke', 'Revoking…', { 'data-revoke': t.id }, '/documents/' + id + '/mcp-tokens/' + t.id)
        ]));
      });
    }
    function load() {
      return api('GET', '/documents/' + id + '/mcp-tokens').then(function (r) { drawList(r.tokens || []); })
        .catch(function () { listBox.textContent = ''; listBox.appendChild(el('p', { class: 'mcp-empty', text: 'Only the owner of this diagram can manage MCP tokens.' })); sec.classList.add('mcp-locked'); });
    }
    var createBtn = el('button', { class: 'btn primary mcp-create', type: 'button', id: 'mcp-create', text: 'Create token', onclick: function () {
      var name = nameIn.value.trim();
      if (!name) { nameIn.focus(); toast('error', 'Give the token a name'); return; }
      createBtn.disabled = true; createBtn.textContent = 'Creating…';
      api('POST', '/documents/' + id + '/mcp-tokens', { name: name, role: roleSel.value, expiresInDays: parseInt(expSel.value, 10), enableAI: true }).then(function (r) {
        nameIn.value = '';
        var cfg = JSON.stringify({ mcpServers: { drawspace: { type: 'http', url: mcpUrl, headers: { Authorization: 'Bearer ' + r.token } } } }, null, 2);
        created.textContent = '';
        created.appendChild(el('p', { class: 'mcp-warn', text: 'Copy this token now. It is shown only once.' }));
        created.appendChild(el('input', { class: 'field mcp-token', id: 'mcp-token', type: 'text', name: 'mcp-new-token', autocomplete: 'off', spellcheck: 'false', readonly: 'readonly', value: r.token, 'aria-label': 'New token', onfocus: function (ev) { ev.target.select(); } }));
        created.appendChild(el('div', { class: 'mcp-actions' }, [copyBtn('Copy token', r.token), copyBtn('Copy MCP config', cfg)]));
        created.appendChild(el('p', { class: 'mcp-meta', text: 'Endpoint ' + mcpUrl + ' · send the token as “Authorization: Bearer <token>”.' }));
        created.hidden = false;
        return load();
      }).catch(function (e) { toast('error', e.message); }).then(function () { createBtn.disabled = false; createBtn.textContent = 'Create token'; });
    } });
    sec.appendChild(el('p', { class: 'mcp-meta mcp-intro', text: 'A token lets an AI agent or integration work on this diagram through MCP. It opens only this diagram and can be revoked at any time. Creating one allows AI agents on this diagram.' }));
    sec.appendChild(listBox);
    sec.appendChild(created);
    sec.appendChild(el('div', { class: 'mcp-form' }, [nameIn, roleSel, expSel, createBtn]));
    load();
    return sec;
  }

  // ---- Share (ADR-0021): access for somebody new is an INVITATION they accept; nothing here grants access directly -----------------------
  var dateFmtShare = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  function expiryText(t) { return t ? 'expires ' + dateFmtShare.format(new Date(t)) : 'never expires'; }
  // "Expires in" control shared by the three kinds of invitation: presets, Never, and a custom number of days (1..365, like MCP tokens)
  function expiryControl(idPrefix) {
    var sel = el('select', { class: 'field mcp-select', id: idPrefix + '-exp', name: idPrefix + '-expiry', 'aria-label': 'Invitation expiry' }, [
      el('option', { value: '1', text: 'Expires in 1 day' }), el('option', { value: '7', text: 'Expires in 7 days', selected: true }), el('option', { value: '30', text: 'Expires in 30 days' }),
      el('option', { value: '90', text: 'Expires in 90 days' }), el('option', { value: '365', text: 'Expires in 1 year' }), el('option', { value: '0', text: 'Never expires' }), el('option', { value: 'custom', text: 'Custom…' })]);
    sel.value = '7';
    var days = el('input', { class: 'field expiry-days', id: idPrefix + '-days', type: 'number', min: '1', max: '365', value: '14', hidden: true, 'aria-label': 'Number of days (1 to 365)' });
    var note = el('p', { class: 'mcp-meta expiry-note', hidden: true, text: 'Anyone with this link can ask for access until you revoke it.' });
    function sync() { days.hidden = sel.value !== 'custom'; note.hidden = !(sel.value === '0'); }
    sel.addEventListener('change', sync);
    return {
      root: el('div', { class: 'expiry-ctl' }, [sel, days]), note: note,
      value: function () {
        if (sel.value !== 'custom') return parseInt(sel.value, 10);
        var n = parseInt(days.value, 10);
        if (!(n >= 1 && n <= 365)) { days.focus(); throw new Error('Custom expiry must be 1 to 365 days'); }
        return n;
      }
    };
  }
  var inviteErrText = {
    email_cooldown: 'An invitation e-mail for this person and diagram was already sent today. You can send again from 00:00 WIB, or copy the invitation link and send it yourself.',
    no_email: 'No e-mail address is known for this person. Copy the invitation link and send it yourself.',
    email_unavailable: 'E-mail is not configured here. Copy the invitation link instead.',
    already_pending: 'An invitation for this person is already waiting. Use Resend on it.',
    already_has_access: 'This person already has this access or more.',
    too_many_invitations: 'Too many open invitations. Cancel or revoke one first.'
  };
  function inviteError(e) { return inviteErrText[e.code] || e.message; }

  function openShare(id) {
    var perms, invs = [], canEmail = true;
    var wrap = el('div', { class: 'share-dlg' }, []);
    var list = el('div', { id: 'share-list', class: 'share-list' }, []);
    var pendingBox = el('div', { id: 'share-pending', class: 'share-list share-section', hidden: true }, []);
    var linksBox = el('div', { id: 'share-links', class: 'share-list share-section', hidden: true }, []);
    var created = el('div', { id: 'share-created', class: 'mcp-created', hidden: true, role: 'status' }, []);
    var hits = el('div', { class: 'hits' }, []);
    var q = el('input', { class: 'field', id: 'share-q', type: 'search', placeholder: 'Add people by name or username', autocomplete: 'off', 'aria-label': 'Add people' });
    var roleDd = makeRoleDropdown('editor', null, { id: 'share-role', 'aria-label': 'Role' });
    var userExp = expiryControl('share-user');
    var picked = null; // { username, name, hasEmail }
    var pickedBox = el('div', { id: 'share-picked', class: 'share-picked', hidden: true }, []);
    var sendBtn = el('button', { class: 'btn primary', type: 'button', id: 'share-send', text: 'Send invitation', disabled: true });
    var copyUserBtn = el('button', { class: 'btn', type: 'button', id: 'share-copy-user', text: 'Copy invitation link', disabled: true });
    var noEmailNote = el('p', { class: 'mcp-meta', id: 'share-noemail', hidden: true, text: 'No e-mail address was found for this person, so the invitation cannot be sent by e-mail. Copy the link and send it yourself.' });
    var addBox = el('div', { id: 'share-add-box', class: 'share-add-box', hidden: true }, []);
    var addBtn = el('button', { id: 'btn-add-user', class: 'add-user-btn', type: 'button', onclick: function () {
      addBox.hidden = !addBox.hidden;
      if (!addBox.hidden) setTimeout(function () { q.focus(); }, 20);
    } }, [el('span', { class: 'add-user-plus', 'aria-hidden': 'true', text: '+' }), el('span', { text: 'Add user' })]);
    var linkRoleDd = makeRoleDropdown('viewer', null, { id: 'share-link-role', 'aria-label': 'Role for the invitation link' });
    var linkExp = expiryControl('share-link');

    function refresh() {
      return Promise.all([api('GET', '/documents/' + id + '/permissions'), api('GET', '/documents/' + id + '/invitations').catch(function () { return { invitations: [] }; })]).then(function (res) {
        perms = res[0]; invs = res[1].invitations || []; draw();
      });
    }
    function setRole(subject, role) {
      api('PUT', '/documents/' + id + '/permissions', { subject: subject, role: role }).then(refresh).catch(function (e) { toast('error', e.message); });
    }
    function copyBtn(label, text) {
      return el('button', { class: 'btn', type: 'button', text: label, onclick: function (ev) {
        var b = ev.currentTarget;
        navigator.clipboard.writeText(text).then(function () { b.textContent = 'Copied'; setTimeout(function () { b.textContent = label; }, 1500); }, function () { toast('error', 'Copy failed: select the link and copy it manually'); });
      } });
    }
    // a new link is shown right away; pending invitations can be copied again from the list at any time (the server returns the link on request)
    function showCreated(link, who, expiresAt, note) {
      created.textContent = '';
      created.appendChild(el('p', { class: 'mcp-warn', text: note || 'Invitation link created. You can copy it again from the list any time.' }));
      created.appendChild(el('input', { class: 'field mcp-token', id: 'share-new-link', type: 'text', name: 'share-new-link', autocomplete: 'off', spellcheck: 'false', readonly: 'readonly', value: link, 'aria-label': 'Invitation link', onfocus: function (ev) { ev.currentTarget.select(); } }));
      created.appendChild(el('div', { class: 'mcp-actions' }, [copyBtn('Copy link', link)]));
      created.appendChild(el('p', { class: 'mcp-meta', text: (who ? 'For ' + who + ' only · ' : 'Anyone signed in to DrawSpace · ') + expiryText(expiresAt) + '. Access starts only after they accept.' }));
      created.hidden = false;
    }
    function linkOf(token) { return location.origin + '/invite/' + token; }
    var OLD_LINK = 'Older link: it cannot be shown again. Create a new one.';
    // second line of a pending invitation row: the full link in a readonly input plus Copy link; fetched lazily, only when the server says it is recoverable
    function linkLine(v) {
      var line = el('div', { class: 'share-linkline' }, []);
      function older() { line.textContent = ''; line.appendChild(el('span', { class: 'mcp-meta', text: OLD_LINK })); }
      if (!v.recoverable) { older(); return line; }
      line.appendChild(el('input', { class: 'field mcp-token', type: 'text', autocomplete: 'off', spellcheck: 'false', readonly: 'readonly', disabled: 'disabled', value: 'Loading…', 'data-link-loading': v.id, 'aria-label': 'Invitation link' }));
      api('GET', '/documents/' + id + '/invitations/' + v.id + '/link').then(function (r) {
        if (!r || !r.token) throw new Error('no token');
        var link = linkOf(r.token);
        line.textContent = '';
        line.appendChild(el('input', { class: 'field mcp-token', type: 'text', autocomplete: 'off', spellcheck: 'false', readonly: 'readonly', value: link, 'data-link-input': v.id, 'aria-label': 'Invitation link', onfocus: function (ev) { ev.currentTarget.select(); } }));
        var b = copyBtn('Copy link', link); b.setAttribute('data-copy-link', v.id);
        line.appendChild(b);
      }).catch(older);
      return line;
    }
    function armed(label, busy, attrs, fn) { // asks once, inline, before an irreversible action
      return el('button', Object.assign({ class: 'btn danger', type: 'button', text: label, onclick: function (ev) {
        var b = ev.currentTarget;
        if (!b.dataset.armed) { b.dataset.armed = '1'; b.textContent = 'Confirm ' + label.toLowerCase(); setTimeout(function () { delete b.dataset.armed; if (!b.disabled) b.textContent = label; }, 4000); return; }
        b.disabled = true; b.textContent = busy;
        fn().then(refresh).catch(function (e) { b.disabled = false; b.textContent = label; delete b.dataset.armed; toast('error', e.message); });
      } }, attrs));
    }
    function draw() {
      list.textContent = '';
      list.appendChild(el('div', { class: 'share-row' }, [el('span', { class: 'grow', text: perms.owner }), el('span', { class: 'chip', text: 'owner' })]));
      (perms.permissions || []).forEach(function (p) {
        var dd = makeRoleDropdown(p.role, function (newRole) { setRole(p.subject, newRole); }, { 'aria-label': 'Role of ' + p.subject, 'data-subject': p.subject });
        list.appendChild(el('div', { class: 'share-row' }, [
          el('span', { class: 'grow', text: p.subject }), dd,
          el('button', { class: 'btn danger', type: 'button', text: 'Remove', 'data-remove': p.subject, onclick: function () { setRole(p.subject, ''); } })]));
      });
      var users = invs.filter(function (v) { return v.kind === 'user' && v.state !== 'accepted'; });
      var links = invs.filter(function (v) { return v.kind === 'link'; });
      pendingBox.hidden = !users.length; pendingBox.textContent = '';
      if (users.length) pendingBox.appendChild(el('label', { text: 'Invitations' }));
      users.forEach(function (v) {
        var dead = v.state !== 'pending';
        var resend = v.state === 'pending' ? el('button', { class: 'btn', type: 'button', 'data-resend': v.id, text: v.emailSentAt ? 'Resend' : 'New link', onclick: function (ev) {
          var b = ev.currentTarget; b.disabled = true;
          var days; try { days = userExp.value(); } catch (e) { b.disabled = false; toast('error', e.message); return; }
          api('POST', '/documents/' + id + '/invitations/' + v.id + '/resend', { expiresInDays: days }).then(function (r) {
            if (!r.emailed && r.token) showCreated(linkOf(r.token), v.subject, r.invitation.expiresAt, r.emailError === 'send_failed' ? 'The e-mail could not be sent. Copy this link and send it yourself.' : null);
            else toast('info', 'Invitation e-mailed again');
            return refresh();
          }).catch(function (e) { b.disabled = false; toast('error', inviteError(e)); });
        } }) : null;
        pendingBox.appendChild(el('div', { class: 'share-row mcp-row' + (dead ? ' dead' : ''), 'data-invitation': v.id }, [
          el('span', { class: 'grow' }, [el('strong', { text: v.subject }), el('small', { class: 'mcp-meta', text: ' · ' + v.role + ' · ' + (v.emailSentAt ? 'e-mailed' : 'link only') + ' · ' + expiryText(v.expiresAt) })]),
          el('span', { class: 'chip ' + (dead ? 'off' : ''), text: dead ? v.state : 'pending' }), resend,
          armed(dead ? 'Delete' : 'Cancel', dead ? 'Deleting…' : 'Cancelling…', { 'data-cancel': v.id }, function () { return api('DELETE', '/documents/' + id + '/invitations/' + v.id); }),
          dead ? null : linkLine(v)]));
      });
      linksBox.hidden = !links.length; linksBox.textContent = '';
      if (links.length) linksBox.appendChild(el('label', { text: 'Invitation links' }));
      links.forEach(function (v) {
        var dead = v.state !== 'pending';
        linksBox.appendChild(el('div', { class: 'share-row mcp-row' + (dead ? ' dead' : ''), 'data-link': v.id }, [
          el('span', { class: 'grow' }, [el('strong', { text: '…' + v.hint }), el('small', { class: 'mcp-meta', text: ' · ' + v.role + ' · ' + expiryText(v.expiresAt) + ' · used ' + v.useCount + '×' })]),
          el('span', { class: 'chip ' + (dead ? 'off' : ''), text: dead ? v.state : 'active' }),
          armed(dead ? 'Delete' : 'Revoke', dead ? 'Deleting…' : 'Revoking…', { 'data-revoke-link': v.id }, function () { return api('DELETE', '/documents/' + id + '/invitations/' + v.id); }),
          dead ? null : linkLine(v)]));
      });
    }
    function pick(u) {
      picked = u; q.value = ''; hits.textContent = '';
      pickedBox.hidden = false; pickedBox.textContent = '';
      pickedBox.appendChild(el('span', { class: 'chip', text: (u.name ? u.name + ' · ' : '') + u.username }));
      var mail = !!u.hasEmail && canEmail;
      sendBtn.disabled = !mail; copyUserBtn.disabled = false; noEmailNote.hidden = mail;
      if (!mail && !canEmail) noEmailNote.textContent = 'E-mail is not configured here. Copy the invitation link and send it yourself.';
      else noEmailNote.textContent = 'No e-mail address was found for this person, so the invitation cannot be sent by e-mail. Copy the link and send it yourself.';
    }
    function createFor(sendEmail, btn, label) {
      if (!picked) return;
      var days; try { days = userExp.value(); } catch (e) { toast('error', e.message); return; }
      btn.disabled = true; btn.textContent = 'Working…';
      api('POST', '/documents/' + id + '/invitations', { kind: 'user', subject: picked.username, role: roleDd.getValue(), expiresInDays: days, sendEmail: sendEmail }).then(function (r) {
        if (r.emailed) toast('info', 'Invitation e-mailed to ' + picked.username + '. They must accept it to get access.');
        else if (r.token) showCreated(linkOf(r.token), picked.username, r.invitation.expiresAt, sendEmail ? 'The e-mail could not be sent. Copy this link and send it yourself.' : null);
        picked = null; pickedBox.hidden = true; sendBtn.disabled = true; copyUserBtn.disabled = true; noEmailNote.hidden = true; addBox.hidden = true;
        return refresh();
      }).catch(function (e) { toast('error', inviteError(e)); }).then(function () { btn.disabled = !picked; btn.textContent = label; });
    }
    sendBtn.onclick = function () { createFor(true, sendBtn, 'Send invitation'); };
    copyUserBtn.onclick = function () { createFor(false, copyUserBtn, 'Copy invitation link'); };

    var timer = null;
    q.oninput = function () {
      clearTimeout(timer);
      timer = setTimeout(function () {
        var v = q.value.trim(); hits.textContent = '';
        if (v.length < 1) return;
        api('GET', '/users?q=' + encodeURIComponent(v)).then(function (u) {
          canEmail = u.canEmail !== false;
          (u.users || []).filter(function (x) { return x.username !== perms.owner && x.kind !== 'ai'; }).forEach(function (x) {
            hits.appendChild(el('button', { class: 'hit', type: 'button', 'data-user': x.username, text: (x.name ? x.name + ' · ' : '') + x.username + (x.hasEmail ? '' : ' (no e-mail)'), onclick: function () { pick(x); } }));
          });
        }).catch(function () { /* ignore */ });
      }, 200);
    };
    q.addEventListener('keydown', function (e) { if (e.key === 'Enter') e.preventDefault(); }); // Enter in the search box must not close the dialog
    addBox.appendChild(el('div', { class: 'share-add-row' }, [q, roleDd]));
    addBox.appendChild(hits); addBox.appendChild(pickedBox);
    addBox.appendChild(el('div', { class: 'share-add-row' }, [userExp.root])); addBox.appendChild(userExp.note);
    addBox.appendChild(noEmailNote);
    addBox.appendChild(el('div', { class: 'share-add-row' }, [sendBtn, copyUserBtn]));
    var linkBtn = el('button', { class: 'btn', type: 'button', id: 'share-make-link', text: 'Create link', onclick: function () {
      var days; try { days = linkExp.value(); } catch (e) { toast('error', e.message); return; }
      linkBtn.disabled = true;
      api('POST', '/documents/' + id + '/invitations', { kind: 'link', role: linkRoleDd.getValue(), expiresInDays: days }).then(function (r) {
        showCreated(linkOf(r.token), null, r.invitation.expiresAt); return refresh();
      }).catch(function (e) { toast('error', inviteError(e)); }).then(function () { linkBtn.disabled = false; });
    } });
    var linkSec = el('div', { class: 'share-section share-linkmaker' }, [
      el('label', { text: 'Invitation link' }),
      el('p', { class: 'mcp-meta', text: 'Anyone signed in to DrawSpace who opens the link can accept it and gets this access. Send it yourself.' }),
      el('div', { class: 'share-add-row' }, [linkRoleDd, linkExp.root, linkBtn]), linkExp.note]);

    refresh().then(function () {
      wrap.appendChild(el('label', null, ['People with access']));
      wrap.appendChild(list); wrap.appendChild(pendingBox);
      wrap.appendChild(el('div', { class: 'share-add-trigger' }, [addBtn]));
      wrap.appendChild(addBox); wrap.appendChild(linkSec); wrap.appendChild(linksBox); wrap.appendChild(created);
      dialog('Share "' + (doc ? doc.document.title : '') + '"', wrap, [{ label: 'Done', cls: 'primary' }], { wide: true });
    }).catch(function (e) { toast('error', e.message); });
  }

  // ---- invitation page (/invite/<token>): the recipient accepts or declines; the page text is Indonesian by design ----------------------------
  function showInvite(token) {
    var box = $('invite-card'); $('invite-view').hidden = false; document.title = 'Undangan · DrawSpace'; setStatus('auth', 'Signed in');
    function msg(title, text, kind) {
      box.textContent = '';
      box.appendChild(el('h1', { text: title }));
      box.appendChild(el('p', { class: 'invite-text' + (kind ? ' ' + kind : ''), text: text }));
      box.appendChild(el('div', { class: 'invite-actions' }, [el('a', { class: 'btn primary', href: '/', text: 'Ke daftar diagram' })]));
    }
    if (!/^dsi_[A-Za-z0-9_-]{43}$/.test(token)) { msg('Undangan tidak valid', 'Tautan undangan ini tidak lengkap atau sudah rusak. Mintalah tautan baru kepada pengundang.', 'error'); return; }
    var roleLabel = { editor: 'Editor (dapat melihat dan mengubah diagram)', viewer: 'Viewer (hanya dapat melihat diagram)' };
    api('GET', '/invitations/' + encodeURIComponent(token)).then(function (r) {
      box.textContent = '';
      box.appendChild(el('h1', { text: 'Undangan akses diagram' }));
      box.appendChild(el('p', { class: 'invite-text' }, [el('strong', { text: r.inviter.name || r.inviter.username }), ' mengundang Anda untuk mengakses diagram ', el('strong', { text: '“' + r.document.title + '”' }), '.']));
      box.appendChild(el('dl', { class: 'invite-meta' }, [
        el('dt', { text: 'Hak akses' }), el('dd', { id: 'invite-role', text: roleLabel[r.role] || r.role }),
        el('dt', { text: 'Berlaku hingga' }), el('dd', { text: r.expiresAt ? dateFmtShare.format(new Date(r.expiresAt)) : 'Tidak kedaluwarsa' })]));
      if (r.alreadyHas) box.appendChild(el('p', { class: 'invite-text', text: 'Anda sudah memiliki akses pada diagram ini; hak akses yang lebih tinggi tidak akan diturunkan.' }));
      box.appendChild(el('p', { class: 'invite-text muted', text: 'Diagram baru muncul di daftar Anda setelah Anda menekan Terima.' }));
      var acc = el('button', { class: 'btn primary', id: 'invite-accept', type: 'button', text: 'Terima' });
      var dec = el('button', { class: 'btn', id: 'invite-decline', type: 'button', text: 'Tolak' });
      box.appendChild(el('div', { class: 'invite-actions' }, [acc, dec]));
      function fail(e) { msg('Undangan tidak dapat diproses', e.message, 'error'); }
      acc.onclick = function () { acc.disabled = dec.disabled = true; api('POST', '/invitations/' + encodeURIComponent(token) + '/accept').then(function (a) { location.replace('/d/' + encodeURIComponent(a.documentId)); }).catch(fail); };
      dec.onclick = function () { acc.disabled = dec.disabled = true; api('POST', '/invitations/' + encodeURIComponent(token) + '/decline').then(function () { msg('Undangan ditolak', 'Anda menolak undangan ini. Tidak ada akses yang diberikan.'); }).catch(fail); };
    }).catch(function (e) { msg(e.status === 403 ? 'Undangan bukan untuk akun ini' : 'Undangan tidak berlaku', e.message, 'error'); });
  }

  // AI settings as a dropdown under the "AI" menu item (same look as the other menus), not a modal dialog.
  var aiPopAnchor = null;
  function closeAiPop() {
    var p = $('ai-pop'); if (p) p.remove();
    aiPopAnchor = null;
  }
  function openAiSettings(id, anchor) {
    if ($('ai-pop')) { closeAiPop(); return; }
    Promise.all([api('GET', '/documents/' + id + '/permissions'), api('GET', '/documents/' + id + '/mcp-tokens').catch(function () { return null; })]).then(function (res) {
      var r = res[0], tk = res[1]; // tk is null unless the caller is the owner
      var ai = r.aiPolicy || { allowed: false, maxOpsPerMin: 120, maxDelete: 20 };
      var allowed = el('input', { type: 'checkbox', id: 'ai-allowed' }); allowed.checked = !!ai.allowed;
      var rate = el('input', { class: 'ai-num', id: 'ai-rate', type: 'number', min: '1', max: '5000', value: String(ai.maxOpsPerMin), 'aria-label': 'Max AI operations per minute' }); rate.value = String(ai.maxOpsPerMin);
      var del = el('input', { class: 'ai-num', id: 'ai-del', type: 'number', min: '0', max: '1000', value: String(ai.maxDelete), 'aria-label': 'Max AI deletions per change' }); del.value = String(ai.maxDelete);
      var canPolicy = doc && doc.can && doc.can.policy;
      [allowed, rate, del].forEach(function (x) { x.disabled = !canPolicy; });
      var pop = el('div', { id: 'ai-pop', class: 'ai-pop', role: 'dialog', 'aria-label': 'AI collaborators' }, [
        el('label', { class: 'ai-pop-row ai-pop-check', title: 'Let AI agents read and edit this diagram through MCP. Agents connect with a token created under MCP access.' }, [allowed, 'Allow AI agents']),
        el('label', { class: 'ai-pop-row', title: 'Batas jumlah operasi perubahan sel/bentuk dari agen AI per menit (rate limit).' }, [el('span', { class: 'ai-pop-label', text: 'Operations / minute' }), rate]),
        el('label', { class: 'ai-pop-row', title: 'Batas jumlah elemen yang boleh dihapus agen AI dalam satu perubahan; lebih besar perlu konfirmasi.' }, [el('span', { class: 'ai-pop-label', text: 'Deletions / change' }), del]),
      ]);
      if (canPolicy) {
        var saveAi = el('button', { class: 'ai-pop-row ai-pop-save', type: 'button', id: 'save-ai', text: 'Save settings', disabled: true, onclick: function () {
          api('PUT', '/documents/' + id + '/ai-policy', { allowed: allowed.checked, maxOpsPerMin: parseInt(rate.value, 10), maxDelete: parseInt(del.value, 10) })
            .then(function () { toast('info', 'AI settings saved'); saveAi.disabled = true; }).catch(function (e) { toast('error', e.message); });
        } });
        [allowed, rate, del].forEach(function (x) { x.addEventListener('input', function () { saveAi.disabled = false; }); x.addEventListener('change', function () { saveAi.disabled = false; }); });
        pop.appendChild(el('div', { class: 'ai-pop-sep' }));
        pop.appendChild(saveAi);
      } else {
        pop.appendChild(el('div', { class: 'ai-pop-sep' }));
        pop.appendChild(el('div', { class: 'ai-pop-note', text: 'Only the owner or an administrator can change these.' }));
      }
      if (tk) {
        var live = (tk.tokens || []).filter(function (t) { return !t.revokedAt && !(t.expiresAt && new Date(t.expiresAt) < new Date()); }).length;
        pop.appendChild(el('div', { class: 'ai-pop-sep' }));
        pop.appendChild(el('button', { class: 'ai-pop-row ai-pop-mcp', type: 'button', id: 'ai-mcp', onclick: function () { closeAiPop(); openMcpAccess(id); } }, [
          el('span', { class: 'ai-pop-label', text: 'MCP access…' }), el('span', { class: 'ai-pop-count', text: live ? live + ' active' : '' })]));
      }
      document.body.appendChild(pop);
      // anchor under the "AI" item (the editor lives in an iframe, so add the frame offset)
      var f = $('editor'), fr = f ? f.getBoundingClientRect() : { left: 0, top: 0 }, ar = anchor ? anchor.getBoundingClientRect() : { left: 0, bottom: 0 };
      var left = Math.max(8, Math.min(fr.left + ar.left, window.innerWidth - pop.offsetWidth - 8));
      pop.style.left = left + 'px';
      pop.style.top = (fr.top + ar.bottom + 4) + 'px';
      aiPopAnchor = anchor || null;
    }).catch(function (e) { toast('error', e.message); });
  }
  document.addEventListener('mousedown', function (e) { var p = $('ai-pop'); if (p && !p.contains(e.target)) closeAiPop(); });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape') closeAiPop(); });

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
      else if (/^\/invite\/[^/]+\/?$/.test(location.pathname)) showInvite(decodeURIComponent(location.pathname.split('/')[2]));
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
