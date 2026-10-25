// Client-side document model: the exact mirror of internal/model (Go). The server is authoritative; the browser applies
// RESOLVED ops in server order to its "confirmed" state, and authored ops locally (with a local resolve) to predict
// the pending state. Canon() is byte-identical to the Go Canon() so both sides can compare SHA-256 checksums.
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.DrawioModel = factory();
})(typeof self !== 'undefined' ? self : this, function () {
  'use strict';

  // ---- numbers, escaping ----------------------------------------------------------------------------------
  function num(v) {
    if (!isFinite(v)) throw new Error('non-finite number');
    if (v === 0) return '0';
    var s = String(v);
    if (s.indexOf('e') < 0) return s;
    var m = /^(-?)(\d)(?:\.(\d+))?e([+-]\d+)$/.exec(s);
    var sign = m[1], digits = m[2] + (m[3] || ''), exp = parseInt(m[4], 10), point = 1 + exp;
    if (point <= 0) return sign + '0.' + '0'.repeat(-point) + digits;
    if (point >= digits.length) return sign + digits + '0'.repeat(point - digits.length);
    return sign + digits.slice(0, point) + '.' + digits.slice(point);
  }
  function round4(x) { return Math.floor(x * 1e4 + 0.5) / 1e4; }
  function esc(s) {
    return String(s).replace(/\\/g, '\\\\').replace(/\|/g, '\\|').replace(/;/g, '\\;').replace(/=/g, '\\=').replace(/\n/g, '\\n').replace(/\r/g, '\\r');
  }
  function pf(s) { var f = parseFloat(s); return isNaN(f) ? 0 : f; }

  // ---- fractional index (base 36, no trailing zero) ----------------------------------------------------------
  var DIGITS = '0123456789abcdefghijklmnopqrstuvwxyz';
  function validKey(k) {
    if (!k || k.length > 256 || k[k.length - 1] === '0') return false;
    for (var i = 0; i < k.length; i++) if (DIGITS.indexOf(k[i]) < 0) return false;
    return true;
  }
  function midpoint(a, b) {
    if (b !== '') {
      var n = 0;
      while (n < b.length) {
        var ca = n < a.length ? a[n] : '0';
        if (ca !== b[n]) break;
        n++;
      }
      if (n > 0) return b.slice(0, n) + midpoint(n < a.length ? a.slice(n) : '', b.slice(n));
    }
    var da = a !== '' ? DIGITS.indexOf(a[0]) : 0;
    var db = b !== '' ? DIGITS.indexOf(b[0]) : DIGITS.length;
    if (db - da > 1) return DIGITS[Math.floor((da + db + 1) / 2)];
    if (b.length > 1) return b.slice(0, 1);
    return (a !== '' ? a.slice(0, 1) : '0') + midpoint(a.length > 1 ? a.slice(1) : '', '');
  }
  function between(a, b) {
    a = a || ''; b = b || '';
    if (b !== '' && a >= b) return a + 'i';
    return midpoint(a, b);
  }

  // ---- document -----------------------------------------------------------------------------------------------
  function newDoc() { return { seq: 0, pages: new Map() }; }
  function clone(d) {
    var n = { seq: d.seq, pages: new Map() };
    d.pages.forEach(function (p, id) {
      var cp = { id: p.id, name: p.name, order: p.order, attrs: p.attrs, cells: new Map() };
      p.cells.forEach(function (c, cid) { cp.cells.set(cid, { id: c.id, kind: c.kind, parent: c.parent, order: c.order, props: Object.assign({}, c.props) }); });
      n.pages.set(id, cp);
    });
    return n;
  }
  function fromState(pages, seq) {
    var d = newDoc();
    d.seq = seq || 0;
    (pages || []).forEach(function (sp) {
      var p = { id: sp.id, name: sp.name || '', order: sp.order, attrs: sp.attrs || '', cells: new Map() };
      (sp.cells || []).forEach(function (c) { p.cells.set(c.id, { id: c.id, kind: c.k, parent: c.p, order: c.o, props: Object.assign({}, c.props) }); });
      d.pages.set(sp.id, p);
    });
    return d;
  }
  function cmpOrder(a, b) {
    if (a.order !== b.order) return a.order < b.order ? -1 : 1;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  }
  function sortedPages(d) { return Array.from(d.pages.values()).sort(cmpOrder); }
  function childIndex(p) {
    var idx = new Map();
    p.cells.forEach(function (c) {
      if (c.id === '0') return;
      var l = idx.get(c.parent);
      if (!l) idx.set(c.parent, l = []);
      l.push(c);
    });
    idx.forEach(function (l) { l.sort(cmpOrder); });
    return idx;
  }
  function children(p, parent) { return childIndex(p).get(parent) || []; }
  function absOf(p, c) {
    var x = pf(c.props.gx), y = pf(c.props.gy);
    for (var q = c.parent, n = 0; q && n < 10000; n++) {
      var pc = p.cells.get(q);
      if (!pc) break;
      x += pf(pc.props.gx); y += pf(pc.props.gy);
      q = pc.parent;
    }
    return [round4(x), round4(y)];
  }
  function isDescendant(p, anc, id) {
    for (var q = p.cells.get(id).parent, n = 0; q && n < 10000; n++) {
      if (q === anc) return true;
      var pc = p.cells.get(q);
      if (!pc) return false;
      q = pc.parent;
    }
    return false;
  }
  function relTo(p, parent, ax, ay) {
    var pc = parent && p.cells.get(parent);
    if (!pc) return [ax, ay];
    var a = absOf(p, pc);
    return [round4(ax - a[0]), round4(ay - a[1])];
  }
  function hasGeometry(c) { return c.props.gw !== undefined; }
  function isRel(c) { return c.props.grel === '1'; }

  // ---- canon (CANON-v1) ----------------------------------------------------------------------------------------------
  // opts.ignoreOrder replaces order keys by the sibling index (used to compare a live editor, which has no keys).
  function canon(d, opts) {
    var ign = opts && opts.ignoreOrder, out = [];
    sortedPages(d).forEach(function (p, pi) {
      out.push('P|' + esc(p.id) + '|' + esc(p.name) + '|' + (ign ? String(pi) : p.order) + '|' + esc(p.attrs || '') + '\n');
      var idx = childIndex(p);
      var walk = function (parent) {
        (idx.get(parent) || []).forEach(function (c, i) {
          var keys = Object.keys(c.props).sort();
          var line = 'C|' + esc(c.id) + '|' + esc(parent) + '|' + c.kind + '|' + (ign ? String(i) : c.order) + '|';
          keys.forEach(function (k) { line += esc(k) + '=' + esc(c.props[k]) + ';'; });
          out.push(line + '\n');
          walk(c.id);
        });
      };
      var r = p.cells.get('0');
      if (r) {
        out.push('C|0||' + r.kind + '|' + (ign ? '0' : r.order) + '|\n');
        walk('0');
      }
    });
    return out.join('');
  }
  async function sha256hex(text) {
    var c = (typeof crypto !== 'undefined' && crypto.subtle) ? crypto : require('node:crypto').webcrypto;
    var buf = await c.subtle.digest('SHA-256', new TextEncoder().encode(text));
    return Array.from(new Uint8Array(buf)).map(function (b) { return b.toString(16).padStart(2, '0'); }).join('');
  }
  function hash(d) { return sha256hex(canon(d)); }

  // ---- resolve + apply (mirror of Go Resolve/Apply) -----------------------------------------------------------------
  function resolve(d, o) {
    var q = Object.assign({}, o);
    q.r = {};
    var p = d.pages.get(o.page);
    if (!p) return q;
    var c;
    switch (o.t) {
      case 'setPos':
        c = p.cells.get(o.cell);
        if (c) { var rel = relTo(p, c.parent, o.x, o.y); q.r.rx = num(rel[0]); q.r.ry = num(rel[1]); }
        break;
      case 'reparent':
        c = p.cells.get(o.cell);
        if (c && hasGeometry(c) && !isRel(c)) {
          var a = absOf(p, c), r2 = relTo(p, o.parent, a[0], a[1]);
          q.r.rx = num(r2[0]); q.r.ry = num(r2[1]);
        }
        break;
      case 'deleteCell': {
        var dead = new Set([o.cell]), order = [o.cell];
        for (var changed = true; changed;) {
          changed = false;
          var ids = Array.from(p.cells.keys()).sort();
          for (var i = 0; i < ids.length; i++) {
            var cc = p.cells.get(ids[i]);
            if (!dead.has(cc.id) && cc.parent && dead.has(cc.parent)) { dead.add(cc.id); order.push(cc.id); changed = true; }
          }
        }
        q.r.del = order;
        q.r.clr = [];
        Array.from(p.cells.keys()).sort().forEach(function (id) {
          var e = p.cells.get(id);
          if (!dead.has(id) && e.kind === 'e') ['src', 'tgt'].forEach(function (t) {
            if (e.props[t] !== undefined && dead.has(e.props[t])) q.r.clr.push({ cell: id, end: t, term: e.props[t] });
          });
        });
        q.r.snaps = order.map(function (id) { var x = p.cells.get(id); return { id: x.id, k: x.kind, p: x.parent, o: x.order, props: Object.assign({}, x.props) }; });
        break;
      }
    }
    return q;
  }

  // apply mutates d; o must be resolved (o.r present for setPos/reparent/deleteCell).
  function apply(d, o) {
    var r = o.r || {};
    var p = d.pages.get(o.page), c;
    switch (o.t) {
      case 'addPage':
        if (!d.pages.has(o.page)) d.pages.set(o.page, { id: o.page, name: o.name || '', order: o.order, attrs: o.attrs || '', cells: new Map() });
        break;
      case 'deletePage': d.pages.delete(o.page); break;
      case 'renamePage': if (p) p.name = o.name || ''; break;
      case 'reorderPage': if (p) p.order = o.order; break;
      case 'setPageAttrs': if (p) p.attrs = o.attrs || ''; break;
      case 'addCell':
        if (p && !p.cells.has(o.cell)) p.cells.set(o.cell, { id: o.cell, kind: o.kind, parent: o.parent || '', order: o.order || '', props: Object.assign({}, o.props || {}) });
        break;
      case 'setProp':
        c = p && p.cells.get(o.cell);
        if (c) { if (o.val === undefined || o.val === null) delete c.props[o.key]; else c.props[o.key] = o.val; }
        break;
      case 'setPos':
        c = p && p.cells.get(o.cell);
        if (c && r.rx !== undefined) { c.props.gx = r.rx; c.props.gy = r.ry; }
        break;
      case 'connect':
        c = p && p.cells.get(o.cell);
        if (c) { if (!o.term) delete c.props[o.end]; else c.props[o.end] = o.term; }
        break;
      case 'reorder': c = p && p.cells.get(o.cell); if (c) c.order = o.order; break;
      case 'reparent':
        c = p && p.cells.get(o.cell);
        if (c) { c.parent = o.parent; c.order = o.order; if (r.rx !== undefined && r.rx !== '') { c.props.gx = r.rx; c.props.gy = r.ry; } }
        break;
      case 'deleteCell':
        (r.del || []).forEach(function (id) { if (p) p.cells.delete(id); });
        (r.clr || []).forEach(function (e) { var x = p && p.cells.get(e.cell); if (x) delete x.props[e.end]; });
        break;
      case 'restoreCells':
        (o.cells || []).forEach(function (sc) { if (p) p.cells.set(sc.id, { id: sc.id, kind: sc.k, parent: sc.p, order: sc.o, props: Object.assign({}, sc.props) }); });
        break;
      default: break;
    }
    if (o.seq) d.seq = o.seq;
  }

  // predicted application of an authored op (client side)
  function applyAuthored(d, o) { apply(d, resolve(d, o)); }

  return { num: num, round4: round4, esc: esc, validKey: validKey, between: between, newDoc: newDoc, clone: clone, fromState: fromState,
    sortedPages: sortedPages, childIndex: childIndex, children: children, absOf: absOf, relTo: relTo, isDescendant: isDescendant,
    hasGeometry: hasGeometry, isRel: isRel, canon: canon, hash: hash, sha256hex: sha256hex, resolve: resolve, apply: apply, applyAuthored: applyAuthored, cmpOrder: cmpOrder };
});
