// Live presence (SPIKE-003): what collaborators are doing right now, shown on the unmodified draw.io editor.
// Strictly ephemeral: it is NOT document state. Nothing here enters the operation log, history, versions or MCP, and it is
// never a lock; the semantic operation layer still resolves every conflict. Local activity is read from documented mxGraph
// APIs through the host-injected EditorUi (selection model, editing events, graph handler / vertex handler while the mouse is
// down); remote activity is drawn into the editor's own overlay pane (SVG), so it follows scroll and zoom for free.
(function () {
  'use strict';

  var PALETTE = ['#d6336c', '#1c7ed6', '#2f9e44', '#e8590c', '#7048e8', '#0c8599', '#a61e4d', '#5c940d'];
  var HEARTBEAT_MS = 3000, MOVE_MS = 50, DRAFT_MS = 150, SEL_MS = 40, REDRAW_MS = 100;

  function colorFor(user) {
    var h = 0;
    for (var i = 0; i < user.length; i++) h = (h * 31 + user.charCodeAt(i)) >>> 0;
    return PALETTE[h % PALETTE.length];
  }
  var VERB = { selecting: 'selected', editing: 'editing', dragging: 'moving', resizing: 'resizing', connecting: 'connecting', applying: 'applying changes' };

  // opts: { ui, send(act), now() }
  function create(opts) {
    var ui = opts.ui, g = ui.editor.graph, model = g.getModel(), win = g.container.ownerDocument.defaultView, mx = win.mxEvent;
    var SVG = 'http://www.w3.org/2000/svg';
    var remote = new Map(); // sid -> act (from the server)
    var last = { state: 'idle' }, lastSentJson = '', editing = null, dragging = false, resizing = null, dead = false;
    var timers = { move: 0, draft: 0, sel: 0 }, sentCount = 0, drawn = [];

    function pageId() { return ui.currentPage ? ui.currentPage.getId() : ''; }
    function ids(cells) { return (cells || []).map(function (c) { return c.getId(); }); }

    // ---- local detection ---------------------------------------------------------------------------------------------------------
    function current() {
      if (editing) return { state: 'editing', page: pageId(), cells: [editing.cell], draft: editing.draft };
      if (resizing) return { state: resizing.edge ? 'connecting' : 'resizing', page: pageId(), cells: [resizing.cell], preview: resizing.preview };
      if (dragging) return { state: 'dragging', page: pageId(), cells: dragging.cells, preview: { dx: dragging.dx, dy: dragging.dy } };
      var sel = ids(g.getSelectionCells());
      return sel.length ? { state: 'selecting', page: pageId(), cells: sel } : { state: 'idle' };
    }
    function flush(force) {
      if (dead) return;
      var a = current(), j = JSON.stringify(a);
      if (!force && j === lastSentJson) return;
      lastSentJson = j; last = a; sentCount++;
      opts.send(a);
    }
    function later(kind, ms) {
      if (timers[kind]) return;
      timers[kind] = setTimeout(function () { timers[kind] = 0; flush(false); }, ms);
    }

    g.getSelectionModel().addListener(mx.CHANGE, function () { later('sel', SEL_MS); });

    g.addListener(mx.EDITING_STARTED, function (s, e) {
      var cell = e.getProperty('cell');
      if (!cell) return;
      editing = { cell: cell.getId(), draft: '' };
      // the in-place editor is a contentEditable element created right after this event
      setTimeout(function () {
        var ta = g.cellEditor && g.cellEditor.textarea;
        if (!editing || !ta) return;
        var read = function () { editing.draft = String(ta.innerText || '').slice(0, 500); later('draft', DRAFT_MS); };
        read();
        ta.addEventListener('input', read);
        ta.addEventListener('keyup', read);
      }, 0);
      flush(false);
    });
    g.addListener(mx.EDITING_STOPPED, function () { editing = null; flush(false); });

    // drag / resize: observed while the mouse is down; nothing is sent per pixel, only throttled previews
    g.addMouseListener({
      mouseDown: function () {},
      mouseMove: function () {
        var gh = g.graphHandler, changed = false;
        if (gh && gh.first && gh.cells && gh.cells.length && (gh.currentDx || gh.currentDy)) {
          var sc = g.view.scale;
          dragging = { cells: ids(gh.cells).slice(0, 50), dx: Math.round(gh.currentDx / sc * 100) / 100, dy: Math.round(gh.currentDy / sc * 100) / 100 };
          changed = true;
        } else if (dragging) { dragging = null; changed = true; }
        var found = null;
        var hs = g.selectionCellsHandler && g.selectionCellsHandler.handlers;
        if (hs && hs.visit) hs.visit(function (k, h) {
          if (h && h.index != null && h.bounds && h.state && h.state.cell) found = h;
        });
        if (found) {
          var v = g.view, b = found.bounds, sc2 = v.scale;
          resizing = { cell: found.state.cell.getId(), edge: !!found.state.cell.edge,
            preview: { x: Math.round((b.x / sc2 - v.translate.x) * 100) / 100, y: Math.round((b.y / sc2 - v.translate.y) * 100) / 100, w: Math.round(b.width / sc2 * 100) / 100, h: Math.round(b.height / sc2 * 100) / 100 } };
          changed = true;
        } else if (resizing) { resizing = null; changed = true; }
        if (changed) later('move', MOVE_MS);
      },
      mouseUp: function () {
        if (dragging || resizing) { dragging = null; resizing = null; later('sel', SEL_MS); }
      }
    });

    var paused = false;
    var hb = setInterval(function () { if (!paused && last.state !== 'idle') flush(true); }, HEARTBEAT_MS); // keeps our entry alive on the server

    // ---- remote rendering ------------------------------------------------------------------------------------------------------------
    function el(name, attrs) {
      var e = win.document.createElementNS(SVG, name);
      Object.keys(attrs).forEach(function (k) { e.setAttribute(k, attrs[k]); });
      return e;
    }
    var layer = null;
    function ensureLayer() {
      var pane = g.view.getOverlayPane();
      if (!pane) return null;
      if (!layer || layer.parentNode !== pane) { layer = el('g', { 'data-presence-layer': '1', 'pointer-events': 'none' }); pane.appendChild(layer); }
      return layer;
    }
    function label(parent, x, y, color, text, extra) {
      var t = el('text', { x: x + 6, y: y - 7, fill: '#fff', 'font-size': '11', 'font-family': 'Helvetica,Arial,sans-serif', 'font-weight': '600' });
      t.textContent = text;
      parent.appendChild(t);
      var w = Math.max(24, text.length * 6.4 + 12);
      var bg = el('rect', { x: x, y: y - 20, width: w, height: 17, rx: 4, fill: color, 'fill-opacity': '0.95' });
      parent.insertBefore(bg, t);
      if (extra) bg.setAttribute('stroke-dasharray', extra);
    }
    function redraw() {
      if (dead) return;
      var now = (opts.now || Date.now)();
      remote.forEach(function (a, sid) { if (a._until < now) remote.delete(sid); }); // expiry runs on OUR clock (no cross-machine clock skew)
      var lay = ensureLayer();
      if (!lay) return;
      while (lay.firstChild) lay.removeChild(lay.firstChild);
      drawn = [];
      var perCell = {}, page = pageId();
      remote.forEach(function (a) {
        if (a.page && a.page !== page) return;
        var color = colorFor(a.user || a.sid), isAI = a.type === 'ai';
        var who = (isAI ? 'AI ' : '') + (a.name || a.user || '?');
        (a.cells || []).forEach(function (cid, idx) {
          var cell = model.getCell(cid), st = cell && g.view.getState(cell);
          if (!st) return;
          var slot = perCell[cid] = (perCell[cid] || 0) + 1;
          var grp = el('g', { 'data-presence-user': a.user || '', 'data-presence-state': a.state, 'data-presence-cell': cid, 'data-presence-type': a.type || 'human' });
          var x = st.x, y = st.y, w = st.width, h = st.height, dash = isAI ? '6 3' : null;
          var base = { fill: a.state === 'editing' ? color : 'none', 'fill-opacity': '0.10', stroke: color, 'stroke-width': a.state === 'selecting' ? 2 : 3, rx: 3 };
          if (dash) base['stroke-dasharray'] = dash;
          grp.appendChild(el('rect', Object.assign({ x: x - 2, y: y - 2, width: w + 4, height: h + 4 }, base)));
          if (a.state === 'dragging' && a.preview && a.preview.dx != null) { // ghost at the (uncommitted) position
            var sc = g.view.scale, gx = x + a.preview.dx * sc, gy = y + (a.preview.dy || 0) * sc;
            grp.appendChild(el('rect', { x: gx, y: gy, width: w, height: h, rx: 3, fill: color, 'fill-opacity': '0.12', stroke: color, 'stroke-width': 2, 'stroke-dasharray': '5 4' }));
          }
          if (a.state === 'resizing' && a.preview && a.preview.w != null) {
            var v = g.view, rs = v.scale;
            grp.appendChild(el('rect', { x: (a.preview.x + v.translate.x) * rs, y: (a.preview.y + v.translate.y) * rs, width: a.preview.w * rs, height: a.preview.h * rs, rx: 3, fill: color, 'fill-opacity': '0.10', stroke: color, 'stroke-width': 2, 'stroke-dasharray': '5 4' }));
          }
          if (idx === 0 || (a.cells || []).length <= 3) label(grp, x - 2, y - 2 - (slot - 1) * 19, color, who + ' · ' + (VERB[a.state] || a.state), dash);
          if (a.state === 'editing' && a.draft) {
            var d = a.draft.replace(/\s+/g, ' ');
            if (d.length > 60) d = d.slice(0, 57) + '…';
            var bw = Math.max(40, d.length * 6.2 + 14);
            grp.appendChild(el('rect', { x: x - 2, y: y + h + 4, width: bw, height: 18, rx: 4, fill: '#fff', stroke: color, 'stroke-width': 1.5 }));
            var tt = el('text', { x: x + 5, y: y + h + 17, fill: '#222', 'font-size': '11', 'font-family': 'Helvetica,Arial,sans-serif' });
            tt.textContent = '✎ ' + d;
            grp.appendChild(tt);
          }
          lay.appendChild(grp);
          drawn.push({ user: a.user, state: a.state, cell: cid, type: a.type || 'human' });
        });
      });
    }
    var rd = setInterval(function () { if (remote.size || (layer && layer.firstChild)) redraw(); }, REDRAW_MS);
    g.view.addListener(mx.SCALE_AND_TRANSLATE, redraw);
    g.view.addListener(mx.SCALE, redraw);
    g.view.addListener(mx.TRANSLATE, redraw);

    return {
      // message from the server: {sid,user,name,type,state,page,cells,preview,draft,exp}
      remote: function (a) {
        if (!a || !a.sid) return;
        if (a.state === 'gone' || a.state === 'idle') remote.delete(a.sid);
        else { a._until = (opts.now || Date.now)() + (a.type === 'ai' ? 4500 : 9000); remote.set(a.sid, a); }
        redraw();
      },
      clearRemote: function () { remote.clear(); redraw(); },
      list: function () { return Array.from(remote.values()); },
      rendered: function () { return drawn.slice(); },
      localState: function () { return last; },
      sent: function () { return sentCount; },
      stop: function () { dead = true; clearInterval(hb); clearInterval(rd); remote.clear(); if (layer && layer.parentNode) layer.parentNode.removeChild(layer); },
      pauseHeartbeat: function (v) { paused = !!v; }, // test hook: simulates a browser that froze without closing its socket
      selfIdle: function () { editing = dragging = resizing = null; flush(true); }
    };
  }

  window.DrawioPresence = { create: create, colorFor: colorFor };
})();
