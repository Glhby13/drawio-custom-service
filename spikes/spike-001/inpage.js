// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-001). Injected into host.html by run.mjs.
// Edit operations that stand in for user actions (through draw.io's own Graph API inside the iframe),
// a serializer for captured mxGraph changes, and a canonical snapshot used to compare replicas.
(function () {
  const win = (n) => H.frames[n].contentWindow;
  const g = (n) => H.ui[n].editor.graph;
  const cell = (n, id) => { const c = g(n).model.getCell(id); if (!c) throw new Error(`no cell ${id} in ${n}`); return c; };

  window.OPS = {
    move: (n, id, dx, dy) => g(n).moveCells([cell(n, id)], dx, dy),
    resize: (n, id, w) => { const m = g(n).model, c = cell(n, id), geo = c.geometry.clone(); geo.width = w; m.setGeometry(c, geo); },
    label: (n, id, v) => g(n).model.setValue(cell(n, id), v),
    style: (n, id, k, v) => g(n).setCellStyles(k, v, [cell(n, id)]),
    attr: (n, id, k, v) => g(n).setAttributeForCell(cell(n, id), k, v),
    del: (n, id) => g(n).removeCells([cell(n, id)], true),
    add: (n, id, x, y) => g(n).insertVertex(g(n).getDefaultParent(), id, id, x, y, 80, 40, 'rounded=1;'),
    connect: (n, id, s, t) => g(n).insertEdge(g(n).getDefaultParent(), id, '', cell(n, s), cell(n, t)),
    disconnect: (n, id) => g(n).model.setTerminal(cell(n, id), null, false),
    group: (n, ids) => { const grp = g(n).groupCells(null, 0, ids.map((i) => cell(n, i))); return grp.id; },
    reparent: (n, id, p) => g(n).model.add(cell(n, p), cell(n, id)),
    toBack: (n, id) => g(n).orderCells(true, [cell(n, id)]),
    toFront: (n, id) => g(n).orderCells(false, [cell(n, id)]),
    addPage: (n, name) => { const ui = H.ui[n]; const p = ui.createPage(name, ui.createPageId()); ui.insertPage(p); return p.getId(); },
    renamePage: (n, idx, name) => { const ui = H.ui[n], w = win(n); ui.editor.graph.model.execute(new w.RenamePage(ui, ui.pages[idx], name)); },
    selectPage: (n, idx) => H.ui[n].selectPage(H.ui[n].pages[idx]),
    undo: (n) => H.ui[n].editor.undoManager.undo(),
  };

  // Serializes the atomic changes of one mxUndoableEdit (classes are globals inside the iframe).
  window.serializeChanges = function (n, edit) {
    const w = win(n);
    const id = (c) => (c == null ? null : c.id);
    const enc = (o) => { try { return w.mxUtils.getXml(new w.mxCodec().encode(o)); } catch (e) { return null; } };
    return edit.changes.map((ch) => {
      const k = [['mxRootChange', w.mxRootChange], ['mxChildChange', w.mxChildChange], ['mxTerminalChange', w.mxTerminalChange],
        ['mxValueChange', w.mxValueChange], ['mxStyleChange', w.mxStyleChange], ['mxGeometryChange', w.mxGeometryChange],
        ['mxCollapseChange', w.mxCollapseChange], ['mxVisibleChange', w.mxVisibleChange], ['mxCellAttributeChange', w.mxCellAttributeChange],
        ['ChangePage', w.ChangePage], ['RenamePage', w.RenamePage], ['SelectPage', w.SelectPage]].find(([, C]) => C && ch instanceof C);
      const type = k ? k[0] : 'unknown:' + (ch.constructor && ch.constructor.name);
      const o = { type };
      if (type === 'mxChildChange') Object.assign(o, { child: id(ch.child), parent: id(ch.parent), previous: id(ch.previous), index: ch.index, previousIndex: ch.previousIndex });
      else if (type === 'mxTerminalChange') Object.assign(o, { cell: id(ch.cell), terminal: id(ch.terminal), previous: id(ch.previous), source: ch.source });
      else if (type === 'mxValueChange') Object.assign(o, { cell: id(ch.cell), value: (ch.value && ch.value.nodeType) ? w.mxUtils.getXml(ch.value) : ch.value });
      else if (type === 'mxStyleChange') Object.assign(o, { cell: id(ch.cell), style: ch.style, previous: ch.previous });
      else if (type === 'mxGeometryChange') Object.assign(o, { cell: id(ch.cell), geometry: enc(ch.geometry) });
      else if (type === 'mxCellAttributeChange') Object.assign(o, { cell: id(ch.cell), attribute: ch.attribute, value: ch.value });
      else if (type === 'mxCollapseChange' || type === 'mxVisibleChange') Object.assign(o, { cell: id(ch.cell) });
      else if (type === 'ChangePage') Object.assign(o, { page: ch.relatedPage && ch.relatedPage.getId(), index: ch.index, previousIndex: ch.previousIndex });
      else if (type === 'RenamePage') Object.assign(o, { page: ch.page && ch.page.getId(), name: ch.name });
      else if (type === 'SelectPage') Object.assign(o, { page: ch.relatedPage && ch.relatedPage.getId() });
      o.codecXmlBytes = (enc(ch) || '').length;
      return o;
    });
  };

  window.CAP = {};
  window.capture = function (n) {
    CAP[n] = [];
    const w = win(n);
    g(n).model.addListener(w.mxEvent.CHANGE, (s, evt) => { CAP[n].push(serializeChanges(n, evt.getProperty('edit'))); });
  };

  // Canonical, order-preserving snapshot of every page: compared across replicas for convergence.
  window.canon = function (n) {
    const ui = H.ui[n], w = win(n);
    const out = [];
    const walk = (c, acc, depth) => {
      const v = c.value;
      acc.push({ id: c.id, parent: c.parent ? c.parent.id : null, depth,
        value: (v && v.nodeType) ? w.mxUtils.getXml(v) : (v == null ? null : String(v)),
        style: c.style || null, vertex: !!c.vertex, edge: !!c.edge,
        source: c.source ? c.source.id : null, target: c.target ? c.target.id : null,
        geometry: c.geometry ? w.mxUtils.getXml(new w.mxCodec().encode(c.geometry)) : null });
      for (let i = 0; i < c.getChildCount(); i++) walk(c.getChildAt(i), acc, depth + 1);
    };
    for (const p of ui.pages) {
      const root = (p === ui.currentPage) ? ui.editor.graph.model.root : p.root;
      const cells = [];
      if (root) walk(root, cells, 0);
      out.push({ page: p.getId(), name: p.getName(), cells });
    }
    return out;
  };
  window.checksum = (n) => H.ui[n].getHashValueForPages(H.ui[n].pages);
})();
