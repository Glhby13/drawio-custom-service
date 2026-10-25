(function () {
  'use strict';
  var f = document.getElementById('f'), err = document.getElementById('err'), btn = document.getElementById('submit');
  var q = new URLSearchParams(location.search), back = q.get('return_to') || '/';
  if (back.charAt(0) !== '/' || back.slice(0, 2) === '//') back = '/';
  document.getElementById('sso').href = '/login/sso?return_to=' + encodeURIComponent(back);
  function show(msg) { err.textContent = msg; err.hidden = !msg; }
  document.getElementById('toggle').addEventListener('click', function () {
    var p = document.getElementById('password'), on = p.type === 'password';
    p.type = on ? 'text' : 'password'; this.textContent = on ? 'Hide' : 'Show'; this.setAttribute('aria-label', on ? 'Hide password' : 'Show password');
  });
  f.addEventListener('submit', function (e) {
    e.preventDefault();
    var u = document.getElementById('username').value.trim(), p = document.getElementById('password').value;
    if (!u || !p) { show('Enter your username and password.'); return; }
    show(''); btn.disabled = true; btn.textContent = 'Signing in…';
    fetch('/login/local', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: u, password: p, return_to: back }) })
      .then(function (r) { return r.json().catch(function () { return {}; }).then(function (j) { return { status: r.status, j: j }; }); })
      .then(function (x) {
        if (x.status === 200 && x.j.redirect) { location.href = x.j.redirect; return; }
        btn.disabled = false; btn.textContent = 'Sign in';
        if (x.status === 429) show('Too many attempts. Wait a few minutes and try again.');
        else if (x.status === 401) { show('Invalid username or password.'); document.getElementById('password').select(); }
        else show('Sign-in is unavailable right now. Try again in a moment.');
      })
      .catch(function () { btn.disabled = false; btn.textContent = 'Sign in'; show('Could not reach the server. Check your connection.'); });
  });
  if (q.get('error') === 'sso') show('Single sign-on was not completed. Try again or use a local account.');
})();
