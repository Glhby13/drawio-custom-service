// Phase 2 STUB of the host-page adapter (ADR-0001 boundary: official embed protocol only).
// It proves same-origin plugin loading under CSP 'self' and answers the embed handshake so the editor
// opens a blank diagram. No collaboration, no persistence, no semantic operations yet (Phases 3-5).
(function () {
  'use strict';
  var frame = document.getElementById('editor');
  var BLANK = '<mxfile><diagram id="p1" name="Page-1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/></root></mxGraphModel></diagram></mxfile>';
  function send(msg) { frame.contentWindow.postMessage(JSON.stringify(msg), location.origin); }
  window.addEventListener('message', function (ev) {
    if (ev.origin !== location.origin || ev.source !== frame.contentWindow) return;
    var msg;
    try { msg = JSON.parse(ev.data); } catch (e) { return; }
    if (msg.event === 'init') {
      send({ action: 'load', autosave: 1, xml: BLANK });
      window.dispatchEvent(new CustomEvent('drawio-adapter-ready'));
    }
  });
  window.__drawioAdapter = { phase: 2, stub: true };
})();
