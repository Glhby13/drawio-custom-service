// Bridge between the document model (model.js) and the unmodified draw.io editor:
//   diff(T, L)    semantic ops that turn the predicted state T into the state L read from the live editor
//   toPatch(L, T) draw.io native patch ({u,i,r}, the format of the embed 'patch' action) that turns the editor into T
// Pure functions: no DOM, no editor access (adapter.js extracts L and posts the patch). Tested in Node (webtest/).
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory(require('./model.js'));
  else root.DrawioBridge = factory(root.DrawioModel);
})(typeof self !== 'undefined' ? self : this, function (M) {
  'use strict';

  var opCounter = 0;
  var opPrefix = (function () {
    var a = new Uint8Array(6);
    (typeof crypto !== 'undefined' ? crypto : require('node:crypto').webcrypto).getRandomValues(a);
    return Array.from(a).map(function (b) { return b.toString(36).padStart(2, '0'); }).join('');
  })();
  function opId() { opCounter++; return 'o' + opPrefix + opCounter.toString(36); }

  // ---- Live: the editor's state as read by the adapter ---------------------------------------------------------
  // { pages: [ { id, name, attrs, cells: Map(id -> {id, kind, parent, order:'', props}), kids: Map(parent -> [ids in z-order]) } ] }
  function docToLive(d) {
    var L = { pages: [] };
    M.sortedPages(d).forEach(function (p) {
      var lp = { id: p.id, name: p.name, attrs: p.attrs, cells: new Map(), kids: new Map() };
      p.cells.forEach(function (c, id) { lp.cells.set(id, { id: id, kind: c.kind, parent: c.parent, order: '', props: Object.assign({}, c.props) }); });
      M.childIndex(p).forEach(function (list, parent) { lp.kids.set(parent, list.map(function (c) { return c.id; })); });
      L.pages.push(lp);
    });
    return L;
  }
  // Doc built from Live; order keys become zero-padded sibling indexes (only meaningful for ignoreOrder comparisons)
  function liveToDoc(L, attrsOf) {
    var d = M.newDoc();
    L.pages.forEach(function (lp, pi) {
      var p = { id: lp.id, name: lp.name, order: String(pi).padStart(6, '0'), attrs: (attrsOf && attrsOf(lp.id)) || lp.attrs || '', cells: new Map() };
      lp.cells.forEach(function (c, id) { p.cells.set(id, { id: id, kind: c.kind, parent: c.parent, order: '', props: Object.assign({}, c.props) }); });
      lp.kids.forEach(function (ids) { ids.forEach(function (id, i) { var c = p.cells.get(id); if (c) c.order = String(i).padStart(6, '0'); }); });
      d.pages.set(lp.id, p);
    });
    return d;
  }

  // ---- order keys --------------------------------------------------------------------------------------------------
  // Longest increasing subsequence over the keys that exist (strictly increasing): those siblings keep their key.
  function lisKeep(keys) {
    var tails = [], tailIdx = [], prev = new Array(keys.length).fill(-1);
    for (var i = 0; i < keys.length; i++) {
      var k = keys[i];
      if (k === undefined) continue;
      var lo = 0, hi = tails.length;
      while (lo < hi) { var m = (lo + hi) >> 1; if (tails[m] < k) lo = m + 1; else hi = m; }
      tails[lo] = k; tailIdx[lo] = i; prev[i] = lo > 0 ? tailIdx[lo - 1] : -1;
    }
    var keep = new Set();
    for (var j = tails.length ? tailIdx[tails.length - 1] : -1; j >= 0; j = prev[j]) keep.add(j);
    return keep;
  }
  // ids in desired order; existing(id) -> current key or undefined. Returns Map(id -> key) for every id.
  function assignKeys(ids, existing) {
    var keys = ids.map(existing);
    var keep = lisKeep(keys), out = new Map();
    var i = 0, left = '';
    while (i < ids.length) {
      if (keep.has(i)) { out.set(ids[i], keys[i]); left = keys[i]; i++; continue; }
      var j = i;
      while (j < ids.length && !keep.has(j)) j++;
      var right = j < ids.length ? keys[j] : '';
      for (var k = i; k < j; k++) { left = M.between(left, right); out.set(ids[k], left); }
      i = j;
    }
    return out;
  }

  // ---- diff: T (model) -> L (live) ---------------------------------------------------------------------------------------
  function diff(T, L) {
    var W = M.clone(T), ops = [];
    var emit = function (o) { o.id = opId(); ops.push(o); M.applyAuthored(W, o); };

    // pages: order keys
    var pageKeys = assignKeys(L.pages.map(function (p) { return p.id; }), function (id) { var tp = T.pages.get(id); return tp ? tp.order : undefined; });
    var lPageIds = new Set(L.pages.map(function (p) { return p.id; }));
    L.pages.forEach(function (lp) {
      var tp = T.pages.get(lp.id);
      if (!tp) emit({ t: 'addPage', page: lp.id, name: lp.name, order: pageKeys.get(lp.id), attrs: '' });
      else {
        if (tp.name !== lp.name) emit({ t: 'renamePage', page: lp.id, name: lp.name });
        if (tp.order !== pageKeys.get(lp.id)) emit({ t: 'reorderPage', page: lp.id, order: pageKeys.get(lp.id) });
      }
    });

    L.pages.forEach(function (lp) {
      var tp = T.pages.get(lp.id);
      var wp = W.pages.get(lp.id);
      // keys for every sibling list in L (existing siblings keep theirs when still increasing)
      var keyOf = new Map();
      lp.kids.forEach(function (ids, parent) {
        assignKeys(ids, function (id) { var tc = tp && tp.cells.get(id); return tc && tc.parent === parent ? tc.order : undefined; }).forEach(function (k, id) { keyOf.set(id, k); });
      });
      var added = [], survivors = [];
      lp.cells.forEach(function (c, id) { if (!tp || !tp.cells.has(id)) added.push(id); else survivors.push(id); });
      // adds: parents (and edge terminals) first
      var pending = new Set(added), progress = true;
      var present = function (id) { return wp.cells.has(id); };
      while (pending.size && progress) {
        progress = false;
        Array.from(pending).forEach(function (id) {
          var c = lp.cells.get(id);
          if (c.parent !== '' && !present(c.parent)) return;
          if (c.kind === 'e' && ((c.props.src && !present(c.props.src) && lp.cells.has(c.props.src)) || (c.props.tgt && !present(c.props.tgt) && lp.cells.has(c.props.tgt)))) return;
          var props = Object.assign({}, c.props);
          if (props.src && !present(props.src)) delete props.src;
          if (props.tgt && !present(props.tgt)) delete props.tgt;
          emit({ t: 'addCell', page: lp.id, cell: id, kind: c.kind, parent: c.parent, order: c.parent === '' ? '' : keyOf.get(id), props: props });
          pending.delete(id); progress = true;
        });
      }
      pending.forEach(function (id) { // cyclic leftovers: add without terminals
        var c = lp.cells.get(id), props = Object.assign({}, c.props);
        delete props.src; delete props.tgt;
        emit({ t: 'addCell', page: lp.id, cell: id, kind: c.kind, parent: c.parent, order: keyOf.get(id) || '', props: props });
      });
      // structure of surviving cells
      survivors.forEach(function (id) {
        var lc = lp.cells.get(id), wc = wp.cells.get(id);
        if (id === '0') return;
        if (wc.parent !== lc.parent) emit({ t: 'reparent', page: lp.id, cell: id, parent: lc.parent, order: keyOf.get(id) });
        else if (wc.order !== keyOf.get(id)) emit({ t: 'reorder', page: lp.id, cell: id, order: keyOf.get(id) });
      });
      // properties of every cell (survivors and adds that the add op could not carry, e.g. terminals)
      // parents before children: moving a parent moves its children, so their absolute positions are compared afterwards
      var seen = new Set(), order = [];
      var dfs = function (id) { if (seen.has(id)) return; seen.add(id); order.push(id); (lp.kids.get(id) || []).forEach(dfs); };
      dfs('0');
      lp.cells.forEach(function (c, id) { if (!seen.has(id)) dfs(id); });
      order.forEach(function (id) {
        if (id === '0') return;
        var wc = wp.cells.get(id), lc = lp.cells.get(id);
        if (wc && lc) propOps(lp, wp, wc, lc, emit);
      });
      // deletes last: roots of removed subtrees only (descendants go with them; survivors were reparented above)
      var dead = new Set();
      wp.cells.forEach(function (c, id) { if (!lp.cells.has(id)) dead.add(id); });
      dead.forEach(function (id) { var dc = wp.cells.get(id); if (dc && id !== '0' && !dead.has(dc.parent)) emit({ t: 'deleteCell', page: lp.id, cell: id }); });
    });
    T.pages.forEach(function (tp, id) { if (!lPageIds.has(id)) emit({ t: 'deletePage', page: id }); });

    var mismatch = M.canon(W, { ignoreOrder: true }) !== M.canon(liveToDoc(L, function (id) { var p = T.pages.get(id); return p && p.attrs; }), { ignoreOrder: true });
    return { ops: ops, predicted: W, mismatch: mismatch };
  }

  function propOps(lp, wp, wc, lc, emit) {
    var rel = M.isRel(lc) || M.isRel(wc);
    var keys = new Set(Object.keys(wc.props).concat(Object.keys(lc.props)));
    var removeAttr = [], removeVal = [], setVal = [], setAttr = [], other = [], connects = [];
    keys.forEach(function (k) {
      if (k === 'gx' || k === 'gy') { if (rel && wc.props[k] !== lc.props[k] && lc.props[k] !== undefined) other.push({ t: 'setProp', page: lp.id, cell: lc.id, key: k, val: lc.props[k] }); return; }
      var wv = wc.props[k], lv = lc.props[k];
      if (wv === lv) return;
      if (k === 'src' || k === 'tgt') { connects.push({ t: 'connect', page: lp.id, cell: lc.id, end: k, term: lv === undefined ? '' : lv }); return; }
      if (lv === undefined) {
        if (k === 'gw' || k === 'gh') return; // geometry never disappears
        var op = { t: 'setProp', page: lp.id, cell: lc.id, key: k };
        if (k.indexOf('va:') === 0) removeAttr.push(op); else if (k === 'v' || k === 'vt') removeVal.push(op); else other.push(op);
      } else {
        var o2 = { t: 'setProp', page: lp.id, cell: lc.id, key: k, val: lv };
        if (k === 'v' || k === 'vt') setVal.push(o2); else if (k.indexOf('va:') === 0) setAttr.push(o2); else other.push(o2);
      }
    });
    [removeAttr, removeVal, setVal, setAttr, other, connects].forEach(function (g) { g.forEach(emit); });
    if (!rel && M.hasGeometry(lc)) { // x/y compare by ABSOLUTE position (a reparent already preserved it on the model side)
      var aw = M.absOf(wp, wc), al = M.absOf(lp, lc);
      if (aw[0] !== al[0] || aw[1] !== al[1]) emit({ t: 'setPos', page: lp.id, cell: lc.id, x: al[0], y: al[1] });
    }
  }

  // ---- native patch: model -> editor ----------------------------------------------------------------------------------
  var XA = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;', '\n': '&#xa;', '\r': '&#xd;', '\t': '&#x9;' };
  function xa(s) { return String(s).replace(/[&<>"'\n\r\t]/g, function (c) { return XA[c]; }); }

  function styleString(c) {
    var parts = [];
    if (c.props.sn) parts.push(c.props.sn);
    Object.keys(c.props).filter(function (k) { return k.indexOf('st:') === 0; }).sort().forEach(function (k) { parts.push(k.slice(3) + '=' + c.props[k]); });
    return parts.length ? parts.join(';') + ';' : '';
  }
  function geometryXml(c) {
    if (c.props.gw === undefined) return null;
    var p = c.props, s = '<mxGeometry';
    [['gx', 'x'], ['gy', 'y']].forEach(function (f) { if (p[f[0]] && p[f[0]] !== '0') s += ' ' + f[1] + '="' + p[f[0]] + '"'; });
    if (p.gw !== '0') s += ' width="' + p.gw + '"';
    if (p.gh && p.gh !== '0') s += ' height="' + p.gh + '"';
    if (p.grel === '1') s += ' relative="1"';
    s += ' as="geometry"';
    if (p.gex) {
      var ex = JSON.parse(p.gex), inner = '';
      var pt = function (name, v) { return '<mxPoint x="' + v[0] + '" y="' + v[1] + '" as="' + name + '"/>'; };
      if (ex.sp) inner += pt('sourcePoint', ex.sp);
      if (ex.tp) inner += pt('targetPoint', ex.tp);
      if (ex.p) inner += '<Array as="points">' + ex.p.map(function (q) { return '<mxPoint x="' + q[0] + '" y="' + q[1] + '"/>'; }).join('') + '</Array>';
      if (ex.off) inner += pt('offset', ex.off);
      if (ex.alt) inner += '<mxRectangle x="' + ex.alt[0] + '" y="' + ex.alt[1] + '" width="' + ex.alt[2] + '" height="' + ex.alt[3] + '" as="alternateBounds"/>';
      return s + '>' + inner + '</mxGeometry>';
    }
    return s + '/>';
  }
  function xmlValue(c) {
    var s = '<' + c.props.vt;
    Object.keys(c.props).filter(function (k) { return k.indexOf('va:') === 0; }).sort().forEach(function (k) { s += ' ' + k.slice(3) + '="' + xa(c.props[k]) + '"'; });
    return s + '/>';
  }

  function nativeCell(c, previous) {
    var o = { id: c.id };
    if (c.kind === 'v') o.vertex = 1;
    if (c.kind === 'e') o.edge = 1;
    if (c.props.fcon === '0') o.connectable = 0;
    if (c.parent) o.parent = c.parent;
    if (previous !== undefined) o.previous = previous;
    if (c.props.src) o.source = c.props.src;
    if (c.props.tgt) o.target = c.props.tgt;
    var st = styleString(c);
    if (st) o.style = st;
    var g = geometryXml(c);
    if (g) o.geometry = g;
    if (c.props.fcol === '1') o.collapsed = 1;
    if (c.props.fvis === '0') o.visible = 0;
    if (c.props.vt) o.xmlValue = xmlValue(c); else if (c.props.v !== undefined) o.value = c.props.v;
    return o;
  }

  function group(c, keys, test) { return keys.filter(test).map(function (k) { return k + '=' + c.props[k]; }).sort().join('\u0001'); }
  var groups = {
    value: function (c) { return String(c.props.vt) + '\u0002' + String(c.props.v) + '\u0002' + group(c, Object.keys(c.props), function (k) { return k.indexOf('va:') === 0; }); },
    style: function (c) { return styleString(c); },
    geometry: function (c) { return String(geometryXml(c)); },
    flags: function (c) { return [c.props.fvis, c.props.fcol, c.props.fcon].join('|'); },
    source: function (c) { return String(c.props.src); },
    target: function (c) { return String(c.props.tgt); },
  };

  function cellXml(c, parent) {
    var wrapped = c.props.vt, s = '';
    if (wrapped) {
      s += '<' + wrapped;
      Object.keys(c.props).filter(function (k) { return k.indexOf('va:') === 0; }).sort().forEach(function (k) { s += ' ' + k.slice(3) + '="' + xa(c.props[k]) + '"'; });
      s += ' id="' + xa(c.id) + '">';
    }
    s += '<mxCell';
    if (!wrapped) { s += ' id="' + xa(c.id) + '"'; if (c.props.v !== undefined) s += ' value="' + xa(c.props.v) + '"'; }
    var st = styleString(c);
    if (st) s += ' style="' + xa(st) + '"';
    if (c.kind === 'v') s += ' vertex="1"'; else if (c.kind === 'e') s += ' edge="1"';
    if (c.props.fvis === '0') s += ' visible="0"';
    if (c.props.fcol === '1') s += ' collapsed="1"';
    if (c.props.fcon === '0') s += ' connectable="0"';
    if (parent) s += ' parent="' + xa(parent) + '"';
    if (c.props.src) s += ' source="' + xa(c.props.src) + '"';
    if (c.props.tgt) s += ' target="' + xa(c.props.tgt) + '"';
    var g = geometryXml(c);
    s += g ? '>' + g + '</mxCell>' : '/>';
    return wrapped ? s + '</' + wrapped + '>' : s;
  }
  function pageXml(p) {
    var idx = M.childIndex(p), out = '<diagram id="' + xa(p.id) + '" name="' + xa(p.name) + '"><mxGraphModel><root>';
    var walk = function (c, parent) { out += cellXml(c, parent); (idx.get(c.id) || []).forEach(function (k) { walk(k, c.id); }); };
    var r = p.cells.get('0');
    if (r) walk(r, '');
    return out + '</root></mxGraphModel></diagram>';
  }

  function toPatch(L, T) {
    var patch = {}, pageU = {}, pageI = [], pageR = [];
    var lById = new Map(L.pages.map(function (p) { return [p.id, p]; }));
    var tPages = M.sortedPages(T);
    tPages.forEach(function (tp, i) {
      var lp = lById.get(tp.id);
      if (!lp) { pageI.push({ id: tp.id, data: pageXml(tp), previous: i > 0 ? tPages[i - 1].id : '' }); return; }
      var pu = {}, cu = {}, ci = [], cr = [];
      if (lp.name !== tp.name) pu.name = tp.name;
      var tIdx = M.childIndex(tp);
      // removed cells
      lp.cells.forEach(function (lc, id) { if (!tp.cells.has(id)) cr.push(id); });
      // does the sibling order need to be re-stated? (any list whose surviving order differs, new or moved cells)
      var needPrev = new Set();
      tIdx.forEach(function (list, parent) {
        var lseq = (lp.kids.get(parent) || []).filter(function (id) { var t = tp.cells.get(id); return t && t.parent === parent; });
        var tseq = list.map(function (c) { return c.id; });
        var same = lseq.length === tseq.filter(function (id) { var l = lp.cells.get(id); return l && l.parent === parent; }).length;
        var order = tseq.filter(function (id) { var l = lp.cells.get(id); return l && l.parent === parent; });
        if (same) for (var k = 0; k < order.length; k++) if (order[k] !== lseq[k]) { same = false; break; }
        if (!same) tseq.forEach(function (id) { needPrev.add(id); });
      });
      // parent-first insertion order
      var walk = function (c, parent) {
        var lc = lp.cells.get(c.id), prevOf = function () { var sib = tIdx.get(parent) || [], i = sib.findIndex(function (x) { return x.id === c.id; }); return i > 0 ? sib[i - 1].id : ''; };
        if (c.id !== '0') {
          if (!lc) ci.push(nativeCell(c, prevOf()));
          else {
            var u = {};
            if (lc.parent !== c.parent) u.parent = c.parent;
            Object.keys(groups).forEach(function (g) {
              if (groups[g](lc) === groups[g](c)) return;
              if (g === 'value') { if (c.props.vt) u.xmlValue = xmlValue(c); else u.value = c.props.v === undefined ? null : c.props.v; }
              else if (g === 'style') u.style = styleString(c) || null;
              else if (g === 'geometry') { var gx = geometryXml(c); if (gx) u.geometry = gx; }
              else if (g === 'flags') { u.visible = c.props.fvis === '0' ? 0 : 1; u.collapsed = c.props.fcol === '1' ? 1 : 0; u.connectable = c.props.fcon === '0' ? 0 : 1; }
              else if (g === 'source') u.source = c.props.src || '';
              else if (g === 'target') u.target = c.props.tgt || '';
            });
            if (needPrev.has(c.id) || lc.parent !== c.parent) u.previous = prevOf();
            if (Object.keys(u).length) cu[c.id] = u;
          }
        }
        (tIdx.get(c.id) || []).forEach(function (k) { walk(k, c.id); });
      };
      var root = tp.cells.get('0');
      if (root) walk(root, '');
      var cells = {};
      if (Object.keys(cu).length) cells.u = cu;
      if (ci.length) cells.i = ci;
      if (cr.length) cells.r = cr;
      if (Object.keys(cells).length) pu.cells = cells;
      if (Object.keys(pu).length) pageU[tp.id] = pu;
    });
    L.pages.forEach(function (lp) { if (!T.pages.has(lp.id)) pageR.push(lp.id); });
    if (Object.keys(pageU).length) patch.u = pageU;
    if (pageI.length) patch.i = pageI;
    if (pageR.length) patch.r = pageR;
    return { patch: patch, empty: Object.keys(patch).length === 0 };
  }

  return { opId: opId, docToLive: docToLive, liveToDoc: liveToDoc, assignKeys: assignKeys, diff: diff, toPatch: toPatch, pageXml: pageXml, cellXml: cellXml, nativeCell: nativeCell, styleString: styleString, geometryXml: geometryXml };
});
