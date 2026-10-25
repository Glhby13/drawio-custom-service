// Host application: document list, editor shell (share, history, versions, comments, export, presence).
// Rendering uses textContent / DOM builders only (the CSP forbids inline script and style attributes).
(function () {
  'use strict';
  var $ = function (id) { return document.getElementById(id); };
  var statusEl = $('status'), csrf = '', me = null, adapter = null, doc = null;

  function setStatus(state, text) { statusEl.dataset.state = state; statusEl.textContent = text; }
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
  function dialog(title, body, actions) {
    $('dlg-title').textContent = title;
    var b = $('dlg-body'), a = $('dlg-actions');
    b.textContent = ''; a.textContent = '';
    b.appendChild(body);
    actions.forEach(function (x) {
      a.appendChild(el('button', { class: 'btn ' + (x.cls || ''), type: 'button', text: x.label, onclick: function () { if (x.fn) { var r = x.fn(); if (r === false) return; } if (!x.keep) $('dlg').close(); } }));
    });
    var d = $('dlg');
    if (!d.open) d.showModal();
  }
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
      document.title = r.document.title + ' · Diagrams';
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
        onSeq: function () {},
      });
      window.__adapter = adapter; // used by the end-to-end suite
    }).catch(function (e) {
      setStatus('error', 'Unavailable');
      var b = $('banner'); b.textContent = e.status === 404 ? 'This diagram does not exist or is not shared with you.' : e.message; b.hidden = false;
      $('btn-share').hidden = $('btn-history').hidden = $('btn-versions').hidden = $('btn-comments').hidden = $('btn-export').hidden = true;
    });

    $('title').onchange = function () {
      var t = this.value.trim(); if (!t) { this.value = doc.document.title; return; }
      api('PATCH', '/documents/' + id, { title: t }).then(function () { doc.document.title = t; document.title = t + ' · Diagrams'; }).catch(function (e) { toast('error', e.message); $('title').value = doc.document.title; });
    };
    $('btn-share').onclick = function () { openShare(id); };
    $('btn-history').onclick = function () { openPanel('History', id, renderHistory); };
    $('btn-versions').onclick = function () { openPanel('Versions', id, renderVersions); };
    $('btn-comments').onclick = function () { openPanel('Comments', id, renderComments); };
    $('btn-export').onclick = function () { openExport(id); };
    $('panel-close').onclick = function () { $('panel').hidden = true; panelRender = null; };
    window.addEventListener('beforeunload', function () { if (adapter) adapter.close(); });
  }

  function applyRole(role) {
    var can = doc && doc.can || {};
    var edit = role === 'editor' || role === 'owner';
    var badge = $('rolebadge');
    badge.hidden = false; badge.dataset.role = role; badge.textContent = role === 'owner' ? 'owner' : edit ? 'can edit' : role === 'commenter' ? 'can comment' : 'view only';
    $('title').readOnly = !(role === 'owner' || can.rename);
    $('btn-share').hidden = !(role === 'owner' || can.share);
    $('btn-comments').hidden = !(role === 'owner' || role === 'editor' || role === 'commenter' || role === 'viewer');
    if (panelRender) panelRender();
  }

  function renderPresence(list, you) {
    var box = $('presence'); box.textContent = '';
    var seen = {};
    list.forEach(function (p) {
      var key = p.user + ':' + p.type + ':' + (p.agent || '');
      if (seen[key]) return; seen[key] = true;
      var label = (p.type === 'ai' ? '🤖 ' + (p.agent || p.user) + (p.obo ? ' (for ' + p.obo + ')' : '') : (p.name || p.user)) + ' · ' + p.role;
      var av = el('span', { class: 'pav' + (p.type === 'ai' ? ' ai' : '') + (p.sid === you ? ' me' : ''), title: label, 'data-user': p.user, text: p.type === 'ai' ? 'AI' : initials(p.name || p.user) });
      box.appendChild(av);
    });
  }

  // ---- side panel ------------------------------------------------------------------------------------------------------
  var panelRender = null;
  function openPanel(title, id, fn) {
    $('panel').hidden = false; $('panel-title').textContent = title;
    panelRender = function () { fn(id, $('panel-body')); };
    panelRender();
  }
  function itemRow(title, sub, buttons) {
    return el('div', { class: 'item' }, [el('div', { class: 'row' }, [el('span', { class: 'grow', text: title })].concat(buttons || [])), el('small', { text: sub })]);
  }
  function renderHistory(id, body) {
    body.textContent = 'Loading…';
    api('GET', '/documents/' + id + '/history?limit=60').then(function (r) {
      body.textContent = '';
      if (!r.history.length) body.appendChild(el('p', { text: 'No changes yet.' }));
      r.history.forEach(function (h) {
        var who = h.actorType === 'ai' ? '🤖 ' + (h.agent || h.actor) + (h.onBehalfOf ? ' for ' + h.onBehalfOf : '') : h.actor;
        var btns = [];
        if (doc.can && doc.can.edit) btns.push(el('button', { class: 'btn', type: 'button', text: 'Revert', 'data-batch': h.batch, onclick: function () {
          ask('Revert change', 'Undo this change by ' + who + '? Other later changes stay.', 'Revert').then(function (ok) {
            if (!ok) return;
            api('POST', '/documents/' + id + '/revert', { batch: h.batch }).then(function (x) { toast('info', 'Reverted ' + x.applied + ' operation(s)' + (x.skipped ? ', skipped ' + x.skipped : '')); renderHistory(id, body); }).catch(function (e) { toast('error', e.message); });
          });
        } }));
        body.appendChild(itemRow(who, h.ops + ' op(s): ' + (h.types || []).join(', ') + ' · ' + ago(h.at), btns));
      });
    }).catch(function (e) { body.textContent = e.message; });
  }
  function renderVersions(id, body) {
    body.textContent = 'Loading…';
    api('GET', '/documents/' + id + '/versions').then(function (r) {
      body.textContent = '';
      if (doc.can && doc.can.edit) body.appendChild(el('button', { class: 'btn primary', type: 'button', id: 'save-version', text: 'Save current as version', onclick: function () {
        prompt2('Save version', 'Name', '').then(function (n) {
          if (!n) return;
          api('POST', '/documents/' + id + '/versions', { name: n }).then(function () { renderVersions(id, body); }).catch(function (e) { toast('error', e.message); });
        });
      } }));
      (r.versions || []).forEach(function (v) {
        var btns = [];
        if (doc.can && doc.can.restore) btns.push(el('button', { class: 'btn', type: 'button', text: 'Restore', onclick: function () {
          ask('Restore version', 'Restore "' + v.name + '"? The current state stays in history and can be reverted.', 'Restore').then(function (ok) {
            if (!ok) return;
            api('POST', '/documents/' + id + '/versions/' + v.id + '/restore', {}).then(function () { toast('info', 'Version restored'); }).catch(function (e) { toast('error', e.message); });
          });
        } }));
        body.appendChild(itemRow(v.name, 'revision ' + v.seq + ' · ' + v.createdBy + ' · ' + ago(v.createdAt), btns));
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

  // ---- share -----------------------------------------------------------------------------------------------------------
  function openShare(id) {
    api('GET', '/documents/' + id + '/permissions').then(function (r) {
      var wrap = el('div', null, []);
      var list = el('div', { id: 'share-list' }, []);
      var hits = el('div', { class: 'hits' }, []);
      var q = el('input', { class: 'field', id: 'share-q', type: 'search', placeholder: 'Add people by name or username', autocomplete: 'off', 'aria-label': 'Add people' });
      var roleSel = el('select', { class: 'field', id: 'share-role', 'aria-label': 'Role' }, ['viewer', 'commenter', 'editor'].map(function (x) { return el('option', { value: x, text: x }); }));
      roleSel.value = 'editor';
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
          var sel = el('select', { class: 'field', 'aria-label': 'Role of ' + p.subject, 'data-subject': p.subject }, ['viewer', 'commenter', 'editor'].map(function (x) { return el('option', { value: x, text: x }); }));
          sel.value = p.role;
          sel.onchange = function () { setRole(p.subject, sel.value); };
          list.appendChild(el('div', { class: 'share-row' }, [el('span', { class: 'grow', text: p.subject === '*' ? 'Everyone with access to Diagrams' : p.subject }), sel,
            el('button', { class: 'btn danger', type: 'button', text: 'Remove', 'data-remove': p.subject, onclick: function () { setRole(p.subject, ''); } })]));
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
                onclick: function () { setRole(x.username, roleSel.value); q.value = ''; hits.textContent = ''; } }));
            });
          }).catch(function () { /* ignore */ });
        }, 200);
      };
      var everyone = el('button', { class: 'btn', type: 'button', id: 'share-everyone', text: 'Share with everyone', onclick: function () { setRole('*', roleSel.value); } });
      wrap.appendChild(el('label', null, ['People with access']));
      wrap.appendChild(list);
      wrap.appendChild(el('div', { class: 'share-row' }, [q, roleSel]));
      wrap.appendChild(hits);
      wrap.appendChild(everyone);
      // AI collaborator policy
      var ai = r.aiPolicy || { allowed: false, maxOpsPerMin: 120, maxDelete: 20 };
      var allowed = el('input', { type: 'checkbox', id: 'ai-allowed' }); allowed.checked = !!ai.allowed;
      var rate = el('input', { class: 'field', id: 'ai-rate', type: 'number', min: '1', max: '5000', value: String(ai.maxOpsPerMin) }); rate.value = String(ai.maxOpsPerMin);
      var del = el('input', { class: 'field', id: 'ai-del', type: 'number', min: '0', max: '1000', value: String(ai.maxDelete) }); del.value = String(ai.maxDelete);
      var canPolicy = doc && doc.can && doc.can.policy;
      [allowed, rate, del].forEach(function (x) { x.disabled = !canPolicy; });
      wrap.appendChild(el('hr'));
      wrap.appendChild(el('label', null, [el('span', null, [allowed, ' Allow AI collaborators (agents) on this diagram'])]));
      wrap.appendChild(el('label', null, ['Max AI operations per minute', rate]));
      wrap.appendChild(el('label', null, ['Max AI deletions per change (larger needs confirmation)', del]));
      var actions = [{ label: 'Close' }];
      if (canPolicy) actions.unshift({ label: 'Save AI policy', cls: 'primary', keep: true, fn: function () {
        api('PUT', '/documents/' + id + '/ai-policy', { allowed: allowed.checked, maxOpsPerMin: parseInt(rate.value, 10), maxDelete: parseInt(del.value, 10) })
          .then(function () { toast('info', 'AI policy saved'); }).catch(function (e) { toast('error', e.message); });
        return false;
      } });
      dialog('Share "' + (doc ? doc.document.title : '') + '"', wrap, actions);
    }).catch(function (e) { toast('error', e.message); });
  }

  function openExport(id) {
    var wrap = el('div', null, [
      el('a', { class: 'btn', id: 'export-xml', href: '/api/v1/documents/' + id + '/content?format=xml&download=1', text: 'draw.io file (.drawio)' }),
      el('a', { class: 'btn', id: 'export-svg', href: '/api/v1/documents/' + id + '/content?format=svg&download=1', text: 'SVG image (first page)' }),
    ]);
    dialog('Export', wrap, [{ label: 'Close' }]);
  }

  // ---- boot ------------------------------------------------------------------------------------------------------------
  fetch('/api/me', { credentials: 'same-origin' })
    .then(function (r) { if (!r.ok) throw new Error('unauthenticated'); return r.json(); })
    .then(function (m) {
      me = m; csrf = m.csrf;
      var name = m.name || m.username || '';
      $('who').textContent = name; $('avatar').textContent = initials(name);
      $('user').title = m.email || name;
      if (Array.isArray(m.roles) && m.roles.indexOf('admin') >= 0) $('role').hidden = false;
      $('user').hidden = false;
      var match = /^\/d\/([^/]+)\/?$/.exec(location.pathname);
      if (match) showEditor(decodeURIComponent(match[1])); else showList();
    })
    .catch(function () { location.href = '/login?return_to=' + encodeURIComponent(location.pathname); });

  $('logout').addEventListener('click', function () {
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
})();
