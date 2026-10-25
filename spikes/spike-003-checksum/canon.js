// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-002 §checksum).
//
// CANON-v0: canonical, order-aware text form of a draw.io document, hashed with SHA-256 (UTF-8).
// Implemented twice (this file for the LIVE mxGraph model in the browser, main.go for draw.io XML); the spike
// proves or disproves that both produce the same bytes.
//
//   doc    := page*                    (page order = document order)
//   page   := "P|" esc(id) "|" esc(name) "\n" cell*   (cells depth-first from the root cell, siblings in z-order)
//   cell   := "C|" id "|" parent "|" kind "|" vis col conn "|" source "|" target "|" value "|" style "|" geometry "\n"
//   kind   := "v" | "e" | "-"           flags are 0/1 digits: visible, collapsed, connectable
//   value  := "N" | "S:"+esc(string) | "X:"+esc(tag)+"{"+ sorted "k=v" joined by "," +"}"   (xml value: attributes except id)
//   style  := tokens without "=" in original order, then key=value pairs sorted by key (duplicate key: last wins)
//   geometry := "x=..;y=..;w=..;h=..;rel=0|1;pts=[x,y x,y];sp=..;tp=..;off=..[;alt=x,y,w,h][;UNRECOGNISED=..]" or "-"
//   number := shortest round-trip decimal, never exponent, "-0" folded to "0"
//   esc    := "\" -> "\\", "|" -> "\|", LF -> "\n", CR -> "\r"   (other code points verbatim, no normalisation)
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.CANON = factory();
})(typeof self !== 'undefined' ? self : this, function () {
  function num(v) {
    if (!isFinite(v)) throw new Error('non-finite number');
    if (v === 0) return '0';
    let s = String(v);
    if (s.indexOf('e') < 0) return s;
    // expand exponent form (JS switches at 1e21 and below 1e-6)
    const m = /^(-?)(\d)(?:\.(\d+))?e([+-]\d+)$/.exec(s);
    const sign = m[1], digits = m[2] + (m[3] || ''), exp = parseInt(m[4], 10);
    const point = 1 + exp; // position of decimal point relative to start of digits
    if (point <= 0) return sign + '0.' + '0'.repeat(-point) + digits;
    if (point >= digits.length) return sign + digits + '0'.repeat(point - digits.length);
    return sign + digits.slice(0, point) + '.' + digits.slice(point);
  }
  const esc = (s) => String(s).replace(/\\/g, '\\\\').replace(/\|/g, '\\|').replace(/\n/g, '\\n').replace(/\r/g, '\\r');
  function style(s) {
    const names = [], kv = new Map();
    for (const tok of String(s || '').split(';')) {
      if (tok === '') continue;
      const i = tok.indexOf('=');
      if (i >= 0) kv.set(tok.slice(0, i), tok.slice(i + 1)); else names.push(tok);
    }
    // byte-wise key order (Go sort.Strings compares bytes; JS default sort compares UTF-16 units: identical for BMP)
    const keys = [...kv.keys()].sort();
    return names.concat(keys.map((k) => k + '=' + kv.get(k))).join(';');
  }
  const pt = (p) => (p ? num(p.x || 0) + ',' + num(p.y || 0) : '-');
  function geo(g) {
    if (!g) return '-';
    let s = 'x=' + num(g.x || 0) + ';y=' + num(g.y || 0) + ';w=' + num(g.width || 0) + ';h=' + num(g.height || 0) + ';rel=' + (g.relative ? 1 : 0);
    s += ';pts=[' + (g.points || []).map(pt).join(' ') + '];sp=' + pt(g.sourcePoint) + ';tp=' + pt(g.targetPoint) + ';off=' + pt(g.offset);
    if (g.alternateBounds) { const a = g.alternateBounds; s += ';alt=' + [a.x || 0, a.y || 0, a.width || 0, a.height || 0].map(num).join(','); }
    return s;
  }
  function value(v) {
    if (v == null) return 'N';
    if (typeof v === 'object' && typeof v.nodeType === 'number') {
      const parts = [];
      for (const a of Array.from(v.attributes)) if (a.name !== 'id') parts.push([a.name, a.value]);
      parts.sort((p, q) => (p[0] < q[0] ? -1 : p[0] > q[0] ? 1 : 0));
      return 'X:' + esc(v.nodeName) + '{' + parts.map(([k, x]) => esc(k) + '=' + esc(x)).join(',') + '}';
    }
    return 'S:' + esc(String(v));
  }
  // `c` is an abstract cell: {id,parent,vertex,edge,visible,collapsed,connectable,source,target,value,style,geometry,children[]}
  function cell(c, out) {
    const kind = c.vertex ? 'v' : c.edge ? 'e' : '-';
    out.push('C|' + esc(c.id) + '|' + esc(c.parent || '') + '|' + kind + '|' + (c.visible ? 1 : 0) + (c.collapsed ? 1 : 0) + (c.connectable ? 1 : 0) +
      '|' + esc(c.source || '') + '|' + esc(c.target || '') + '|' + value(c.value) + '|' + esc(style(c.style)) + '|' + esc(geo(c.geometry)) + '\n');
    for (const k of c.children) cell(k, out);
  }
  function canonical(pages) {
    const out = [];
    for (const p of pages) { out.push('P|' + esc(p.id) + '|' + esc(p.name) + '\n'); if (p.root) cell(p.root, out); }
    return out.join('');
  }
  async function sha256hex(text) {
    const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
    return Array.from(new Uint8Array(buf)).map((b) => b.toString(16).padStart(2, '0')).join('');
  }
  // Extract the abstract model from a LIVE mxCell tree.
  function fromLive(mxRoot) {
    const conv = (c) => ({
      id: c.getId(), parent: c.parent ? c.parent.getId() : '', vertex: !!c.vertex, edge: !!c.edge,
      // NOTE: after decoding from XML these flags are numbers (0/1), on default cells booleans - never compare with === false
      visible: !!c.visible, collapsed: !!c.collapsed, connectable: !!c.connectable,
      source: c.source ? c.source.getId() : '', target: c.target ? c.target.getId() : '',
      value: c.value, style: c.style || '', geometry: c.geometry || null,
      children: (c.children || []).map(conv),
    });
    return conv(mxRoot);
  }
  return { num, esc, style, canonical, sha256hex, fromLive };
});
