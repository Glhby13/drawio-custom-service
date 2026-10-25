(function () {
  'use strict';
  var statusEl = document.getElementById('status');
  var userEl = document.getElementById('user');
  var logoutBtn = document.getElementById('logout');
  var logoutLabel = document.getElementById('logout-label');
  var csrf = '';

  // State lives in data-state (tests and CSS key off it); the text is only presentation.
  function setStatus(state, text) { statusEl.dataset.state = state; statusEl.textContent = text; }

  function initials(name) {
    var parts = String(name || '').trim().split(/[\s._@-]+/).filter(Boolean);
    if (!parts.length) return '?';
    return (parts.length === 1 ? parts[0].slice(0, 2) : parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
  }

  fetch('/api/me', { credentials: 'same-origin' })
    .then(function (r) { if (!r.ok) throw new Error('unauthenticated'); return r.json(); })
    .then(function (me) {
      csrf = me.csrf;
      var name = me.name || me.username || '';
      document.getElementById('who').textContent = name;
      document.getElementById('avatar').textContent = initials(name);
      userEl.title = me.email || name;
      if (Array.isArray(me.roles) && me.roles.indexOf('admin') >= 0) document.getElementById('role').hidden = false;
      userEl.hidden = false;
      if (statusEl.dataset.state !== 'ready') setStatus('auth', 'Signed in');
    })
    .catch(function () { location.href = '/login?return_to=' + encodeURIComponent('/'); });

  window.addEventListener('drawio-adapter-ready', function () { setStatus('ready', 'Editor ready'); });

  logoutBtn.addEventListener('click', function () {
    if (logoutBtn.getAttribute('aria-busy') === 'true') return;
    logoutBtn.setAttribute('aria-busy', 'true');
    logoutLabel.textContent = 'Signing out…';
    fetch('/logout', { method: 'POST', credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf } })
      .then(function (r) { if (!r.ok) throw new Error('logout failed'); return r.json(); })
      .then(function (j) { location.href = j.redirect; })
      .catch(function () {
        logoutBtn.removeAttribute('aria-busy');
        logoutLabel.textContent = 'Sign out';
        setStatus('error', 'Sign-out failed, try again');
      });
  });
})();
