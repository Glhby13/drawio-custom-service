// Host-page adapter between the unmodified draw.io editor (official embed protocol + Draw.loadPlugin on the same-origin
// iframe, ADR-0001) and the platform service (WebSocket, semantic operations, ADR-0006/0010).
//
//   confirmed C   server-ordered document (RESOLVED ops applied in sequence order)
//   pending       batches sent but not yet confirmed; T = C + pending re-applied = what the user should be seeing
//   live L        the editor's real state, read through the EditorUi
//
// reconcile(): L -> diff(T, L) -> ops to the server; then toPatch(L, T) -> embed 'patch' so remote changes appear.
(function () {
  'use strict';
  var M = window.DrawioModel, B = window.DrawioBridge;
  var STYLE_KEY = /^[A-Za-z0-9_.\-]{1,64}$/;

  // ---- live extraction -------------------------------------------------------------------------------------------------
  function n4(x) { return M.num(M.round4(Number(x) || 0)); }
  function pt(p) { return '[' + n4(p.x) + ',' + n4(p.y) + ']'; }

  function geometryProps(g, props) {
    props.gx = n4(g.x); props.gy = n4(g.y); props.gw = n4(g.width); props.gh = n4(g.height);
    if (g.relative) props.grel = '1';
    var parts = [];
    if (g.alternateBounds) parts.push('"alt":[' + [g.alternateBounds.x, g.alternateBounds.y, g.alternateBounds.width, g.alternateBounds.height].map(n4).join(',') + ']');
    if (g.offset) parts.push('"off":' + pt(g.offset));
    if (g.points && g.points.length) parts.push('"p":[' + g.points.map(pt).join(',') + ']');
    if (g.sourcePoint) parts.push('"sp":' + pt(g.sourcePoint));
    if (g.targetPoint) parts.push('"tp":' + pt(g.targetPoint));
    if (parts.length) props.gex = '{' + parts.join(',') + '}';
  }

  function cellProps(c) {
    var props = {}, v = c.value;
    if (v && typeof v === 'object' && typeof v.nodeType === 'number') {
      props.vt = v.nodeName;
      for (var i = 0; i < v.attributes.length; i++) if (v.attributes[i].name !== 'id') props['va:' + v.attributes[i].name] = v.attributes[i].value;
    } else if (v !== null && v !== undefined && v !== '') props.v = String(v);
    var names = [], kv = {};
    String(c.style || '').split(';').forEach(function (tok) {
      if (tok === '') return;
      var i = tok.indexOf('=');
      if (i > 0 && STYLE_KEY.test(tok.slice(0, i))) kv[tok.slice(0, i)] = tok.slice(i + 1); else names.push(tok);
    });
    Object.keys(kv).forEach(function (k) { props['st:' + k] = kv[k]; });
    if (names.length) props.sn = names.join(';');
    if (!c.visible) props.fvis = '0';
    if (c.collapsed) props.fcol = '1';
    if (!c.connectable) props.fcon = '0';
    if (c.source) props.src = c.source.getId();
    if (c.target) props.tgt = c.target.getId();
    if (c.geometry) geometryProps(c.geometry, props);
    return props;
  }

  // Live structure of the editor; page attrs are taken from the model (the editor has no per-page attr we sync yet).
  function fromLive(ui, T) {
    ui.pages.forEach(function (p) { ui.updatePageRoot(p); });
    var L = { pages: [] };
    ui.pages.forEach(function (p) {
      var tp = T && T.pages.get(p.getId());
      var lp = { id: p.getId(), name: p.getName(), attrs: tp ? tp.attrs : '', cells: new Map(), kids: new Map() };
      var walk = function (c, parent) {
        lp.cells.set(c.getId(), { id: c.getId(), kind: c.vertex ? 'v' : c.edge ? 'e' : '-', parent: parent, order: '', props: cellProps(c) });
        var kids = c.children || [];
        if (kids.length) lp.kids.set(c.getId(), kids.map(function (k) { return k.getId(); }));
        kids.forEach(function (k) { walk(k, c.getId()); });
      };
      if (p.root) walk(p.root, '');
      L.pages.push(lp);
    });
    return L;
  }

  // ---- the adapter -------------------------------------------------------------------------------------------------------
  function create(opts) {
    // opts: { frame, docId, onStatus(state,text), onRole(role,title), onPresence(list,you), onFatal(code,msg), onToast(kind,text), onSeq(seq) }
    var frame = opts.frame;
    var ui = null, ws = null, you = '', role = '', closed = false;
    var C = M.newDoc(), pending = [], T = M.newDoc(), E = M.newDoc(), joined = false; // E = what the editor is known to contain
    var busy = false, again = false, backoff = 500, lastHash = null, hashTimer = null, reconnectTimer = null, pingTimer = null;
    var presence = null, offlineEdits = false, loadedInEditor = false, embedReady = false, pendingLoad = null, patchWaiters = [];
    var stats = { sent: 0, acked: 0, nacked: 0, remote: 0, resyncs: 0, patches: 0, hashChecks: 0, hashBad: 0 };

    function status(s, t) { if (opts.onStatus) opts.onStatus(s, t); }
    function canEdit() { return role === 'editor' || role === 'owner'; }
    function rebuild() {
      T = M.clone(C);
      pending.forEach(function (b) { b.ops.forEach(function (o) { try { M.applyAuthored(T, o); } catch (e) { /* server will nack it */ } }); });
    }

    // ---- embed ---------------------------------------------------------------------------------------------------------
    function post(msg) { frame.contentWindow.postMessage(JSON.stringify(msg), location.origin); }
    function waitPatchEvent() {
      return new Promise(function (resolve) {
        var done = false, w = function () { if (!done) { done = true; resolve(); } };
        patchWaiters.push(w);
        setTimeout(w, 4000);
      });
    }
    window.addEventListener('message', function (ev) {
      if (ev.origin !== location.origin || ev.source !== frame.contentWindow) return;
      var msg;
      try { msg = JSON.parse(ev.data); } catch (e) { return; }
      if (msg.event === 'init') { embedReady = true; maybeLoad(); }
      else if (msg.event === 'autosave') { if (loadedInEditor) { if (!joined) offlineEdits = true; schedule(); } }
      else if (msg.event === 'patch') { var ws2 = patchWaiters; patchWaiters = []; ws2.forEach(function (w) { w(); }); }
    });

    function attachUi() {
      return new Promise(function (resolve, reject) {
        var win = frame.contentWindow, tries = 0;
        (function tryIt() {
          if (win.Draw && typeof win.Draw.loadPlugin === 'function') { win.Draw.loadPlugin(function (u) { ui = u; resolve(); }); return; }
          if (++tries > 200) return reject(new Error('Draw.loadPlugin not available'));
          setTimeout(tryIt, 100);
        })();
      });
    }

    function maybeLoad() {
      if (!embedReady || !pendingLoad) return;
      var xml = pendingLoad; pendingLoad = null;
      post({ action: 'load', xml: xml, autosave: 1, diffSync: { patchOnly: true } });
      attachUi().then(function () {
        // the load itself is complete when the first 'load' event arrives; poll until pages exist
        var n = 0;
        (function wait() {
          if (ui.pages && ui.pages.length) { loadedInEditor = true; startPresence(); applyRole(); status('ready', 'All changes saved'); window.dispatchEvent(new CustomEvent('drawio-adapter-ready')); schedule(); return; }
          if (++n > 300) { status('error', 'Editor did not start'); return; }
          setTimeout(wait, 100);
        })();
      }).catch(function () { status('error', 'Editor plugin could not be attached'); });
    }

    function silenceEditorUnloadPrompt() {
      // draw.io thinks it is never "saved" (we persist through the collaboration layer), so it would ask before every page leave.
      // The host asks only when something has really not reached the server (see host.js unsaved()).
      try {
        var w = ui.editor.graph.container.ownerDocument.defaultView;
        w.onbeforeunload = null;
        Object.defineProperty(w, 'onbeforeunload', { configurable: true, get: function () { return null; }, set: function () { /* ignored */ } });
      } catch (e) { /* best effort */ }
    }

    function startPresence() {
      silenceEditorUnloadPrompt();
      if (opts.onEditorReady) { try { opts.onEditorReady(ui, ui.editor.graph.container.ownerDocument.defaultView); } catch (e) { if (window.console) console.error('onEditorReady', e); } }
      if (presence || !window.DrawioPresence) return;
      presence = window.DrawioPresence.create({ ui: ui, send: function (act) { wsSend({ t: 'act', act: act }); } });
    }

    function applyRole() {
      if (!ui) return;
      var g = ui.editor.graph;
      g.setEnabled(canEdit());
      document.body.dataset.role = role;
    }

    // Replace the whole editor content with the server state (first load, resync, restore).
    function loadFull(xml) {
      if (!loadedInEditor) { pendingLoad = xml; maybeLoad(); return Promise.resolve(); }
      post({ action: 'load', xml: xml, autosave: 1, diffSync: { patchOnly: true } });
      return new Promise(function (resolve) {
        var n = 0;
        (function wait() {
          var ok = ui.pages && ui.pages.length && ui.pages[0].getId() === firstPageId(xml);
          if (ok || ++n > 100) return resolve();
          setTimeout(wait, 50);
        })();
      });
    }
    function firstPageId(xml) { var m = /<diagram[^>]*\bid="([^"]*)"/.exec(xml); return m ? m[1] : ''; }

    // ---- reconcile -------------------------------------------------------------------------------------------------------
    function schedule() { if (busy) { again = true; return; } setTimeout(reconcile, 0); }

    async function reconcile() {
      if (busy || !loadedInEditor || !joined) return;
      busy = true;
      try {
        do {
          again = false;
          var L = fromLive(ui, T);
          if (canEdit()) {
            // the user's own changes = live editor vs. what we last put into it (E), never vs. T: remote changes that are
            // not in the editor yet must not look like local deletions
            var d = B.diff(E, L);
            if (d.mismatch && window.console) console.warn('diff does not reproduce the live state (model gap)');
            if (d.ops.length) sendBatch(d.ops);
            offlineEdits = false;
          }
          var r = B.toPatch(L, T);
          E = M.clone(T);
          if (!r.empty) {
            stats.patches++;
            var w = waitPatchEvent();
            post({ action: 'patch', patch: r.patch });
            await w;
          }
        } while (again);
      } catch (e) {
        if (window.console) console.error('reconcile failed', e);
        status('error', 'Sync error, reloading');
        resync('client_error');
      } finally { busy = false; }
    }

    function sendBatch(ops) {
      var batch = 'b' + B.opId();
      var chunks = [];
      for (var i = 0; i < ops.length; i += 400) chunks.push(ops.slice(i, i + 400));
      // a very large edit is split; each chunk is an independent atomic batch (order preserved by the socket)
      chunks.forEach(function (c, i2) {
        var id = i2 === 0 ? batch : batch + '.' + i2;
        pending.push({ batch: id, ops: c, got: 0 });
        stats.sent++;
        wsSend({ t: 'ops', batch: id, base: C.seq, ops: c });
      });
      rebuild();
      status('syncing', pending.length > 1 ? 'Saving… (' + pending.length + ')' : 'Saving…');
    }

    function wsSend(o) { if (ws && ws.readyState === 1) ws.send(JSON.stringify(o)); }

    // ---- server messages -------------------------------------------------------------------------------------------------
    function dropPending(batch) {
      var i = pending.findIndex(function (b) { return b.batch === batch; });
      if (i >= 0) pending.splice(i, 1);
      if (!pending.length && !offlineEdits) { status('ready', 'All changes saved'); try { ui.editor.setModified(false); } catch (e) { /* ignore */ } }
    }

    function onMessage(m) {
      switch (m.t) {
        case 'joined': onJoined(m); break;
        case 'op': onOp(m.op, m.hash); break;
        case 'ack': stats.acked++; dropPending(m.batch); rebuild(); schedule(); break;
        case 'nack':
          stats.nacked++;
          dropPending(m.batch); rebuild();
          if (opts.onToast) opts.onToast('warn', nackText(m));
          schedule();
          break;
        case 'overridden': if (opts.onToast) opts.onToast('info', 'Another collaborator\'s change replaced one of yours'); break;
        case 'resync': resync(m.reason || 'server'); break;
        case 'act': if (presence) presence.remote(m.act); break;
        case 'presence': if (opts.onPresence) opts.onPresence(m.presence || [], you); break;
        case 'role': role = m.role; applyRole(); if (opts.onRole) opts.onRole(role, m.title); schedule(); break;
        case 'error': if (opts.onToast) opts.onToast('warn', m.msg || m.code); break;
        default: break;
      }
    }

    function nackText(m) {
      var map = { forbidden: 'You can no longer edit this diagram', ai_disabled: 'AI editing is disabled for this diagram', rate_limited: 'Too many changes, slow down',
        ai_rate_limited: 'AI quota exceeded', confirmation_required: 'This change deletes many items and needs confirmation' };
      return map[m.code] || ('Change rejected: ' + (m.code || '') + (m.msg ? ' (' + m.msg + ')' : ''));
    }

    function onJoined(m) {
      role = m.role; you = m.you;
      if (opts.onRole) opts.onRole(role, m.title);
      if (m.mode === 'catchup') {
        (m.ops || []).forEach(function (o) { M.apply(C, o); C.seq = o.seq; });
        joined = true; rebuild();
        // resend what was not acknowledged before the connection dropped (idempotent by batch id)
        pending.forEach(function (b) { wsSend({ t: 'ops', batch: b.batch, base: C.seq, ops: b.ops }); });
        applyRole(); schedule();
        if (!pending.length && !offlineEdits && loadedInEditor) status('ready', 'All changes saved');
        return;
      }
      forceFull = false;
      if (presence) presence.clearRemote();
      C = M.fromState(typeof m.state === 'string' ? JSON.parse(m.state) : m.state, m.seq);
      var drop = pending; pending = [];
      if (drop.length && opts.onToast) opts.onToast('info', 'The diagram was reloaded from the server');
      rebuild(); joined = true;
      verify(m.seq, m.hash);
      loadFull(m.xml).then(function () { E = M.clone(C); applyRole(); schedule(); if (loadedInEditor && !pending.length && !offlineEdits) status('ready', 'All changes saved'); });
    }

    function onOp(o, hash) {
      if (!joined || o.seq <= C.seq) return;
      if (o.seq !== C.seq + 1) { resync('gap'); return; }
      M.apply(C, o); C.seq = o.seq;
      stats.remote++;
      if (opts.onSeq) opts.onSeq(C.seq);
      var b = pending.length ? pending.find(function (x) { return x.batch === o.batch; }) : null;
      if (b) { b.got++; if (b.got >= b.ops.length) dropPending(b.batch); }
      if (hash) verify(o.seq, hash);
      rebuild();
      schedule();
    }

    function verify(seq, hash) {
      if (!hash) return;
      lastHash = { seq: seq, hash: hash };
      clearTimeout(hashTimer);
      hashTimer = setTimeout(async function () {
        if (!lastHash || C.seq !== lastHash.seq) return;
        var h = await M.hash(C);
        stats.hashChecks++;
        if (C.seq !== lastHash.seq) return; // moved on meanwhile
        if (h !== lastHash.hash) { stats.hashBad++; resync('hash_mismatch'); }
      }, 150);
    }

    function resync(reason) {
      stats.resyncs++;
      joined = false; forceFull = true;
      try { if (ws) ws.close(4000, 'resync ' + reason); } catch (e) { /* ignore */ }
    }

    // ---- socket ----------------------------------------------------------------------------------------------------------
    function connect() {
      if (closed) return;
      status(joinedOnce ? 'offline' : 'loading', joinedOnce ? 'Offline · reconnecting…' : 'Connecting…');
      var url = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/ws/v1/documents/' + encodeURIComponent(opts.docId);
      var sock = new WebSocket(url);
      ws = sock;
      sock.onopen = function () {
        backoff = 500;
        sock.send(JSON.stringify({ t: 'join', since: loadedInEditor && joinedOnce && !forceFull ? C.seq : 0 }));
        clearInterval(pingTimer);
        pingTimer = setInterval(function () { wsSend({ t: 'ping' }); }, 20000);
      };
      sock.onmessage = function (ev) { try { onMessage(JSON.parse(ev.data)); } catch (e) { if (window.console) console.error(e); } };
      sock.onclose = function (ev) {
        clearInterval(pingTimer);
        if (ws !== sock) return;
        joinedOnce = joinedOnce || joined;
        joined = false;
        if (presence) presence.clearRemote();
        if (closed) return;
        if (ev.code === 4403 || ev.code === 4404) { closed = true; if (opts.onFatal) opts.onFatal(ev.code, ev.code === 4403 ? 'Your access to this diagram was removed' : 'This diagram no longer exists'); return; }
        if (ev.code === 1006 && !joinedOnce) { closed = true; if (opts.onFatal) opts.onFatal(404, 'Diagram not found or not shared with you'); return; }
        status('offline', pending.length || offlineEdits ? 'Offline · changes not saved yet' : 'Offline · reconnecting…');
        reconnectTimer = setTimeout(connect, backoff);
        backoff = Math.min(backoff * 2, 8000);
      };
    }
    var joinedOnce = false, forceFull = false;

    connect();
    return {
      stats: stats,
      presence: function () { return presence; },
      rawAct: function (a) { wsSend({ t: 'act', act: a }); }, // test hook: sends an arbitrary activity report
      state: function () { return { seq: C.seq, pending: pending.length, role: role, joined: joined, you: you, busy: busy, dirty: pending.length > 0 || busy || offlineEdits }; },
      confirmed: function () { return C; },
      predicted: function () { return T; },
      live: function () { return ui ? fromLive(ui, T) : null; },
      hash: function () { return M.hash(C); },
      // order-insensitive checksums of the live editor and of the confirmed/predicted model (used by the e2e suite)
      liveCanonHash: function () { return ui ? M.sha256hex(M.canon(B.liveToDoc(fromLive(ui, T)), { ignoreOrder: true })) : Promise.resolve(''); },
      modelCanonHash: function (which) { return M.sha256hex(M.canon(which === 'predicted' ? T : C, { ignoreOrder: true })); },
      flush: function () { return reconcile(); },
      close: function () { closed = true; if (presence) presence.stop(); clearTimeout(reconnectTimer); try { ws.close(); } catch (e) { /* ignore */ } },
      editorUi: function () { return ui; },
    };
  }

  window.DrawioAdapter = { create: create, fromLive: fromLive };
})();
