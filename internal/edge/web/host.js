(function () {
  'use strict';
  var statusEl = document.getElementById('status');
  var csrf = '';
  fetch('/api/me', { credentials: 'same-origin' })
    .then(function (r) { if (!r.ok) throw new Error('unauthenticated'); return r.json(); })
    .then(function (me) {
      csrf = me.csrf;
      document.getElementById('who').textContent = me.name || me.username;
      statusEl.textContent = 'signed in';
    })
    .catch(function () { location.href = '/login?return_to=' + encodeURIComponent('/'); });
  window.addEventListener('drawio-adapter-ready', function () { statusEl.textContent = 'editor ready'; });
  document.getElementById('logout').addEventListener('click', function () {
    fetch('/logout', { method: 'POST', credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf } })
      .then(function (r) { return r.json(); })
      .then(function (j) { location.href = j.redirect; });
  });
})();
