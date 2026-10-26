(function () {
  'use strict';
  var f = document.getElementById('f');
  var err = document.getElementById('err');
  var btnAD = document.getElementById('submit');
  var btnLocal = document.getElementById('local-btn');
  var totpRow = document.getElementById('totp-row');
  var totpInput = document.getElementById('totp');
  var uInput = document.getElementById('username');
  var pInput = document.getElementById('password');
  var ssoLink = document.getElementById('sso');

  var q = new URLSearchParams(location.search);
  var back = q.get('return_to') || '/';
  if (back.charAt(0) !== '/' || back.slice(0, 2) === '//') back = '/';
  if (ssoLink) ssoLink.href = '/login/sso?return_to=' + encodeURIComponent(back);

  function show(msg) {
    err.textContent = msg;
    err.hidden = !msg;
  }

  document.getElementById('toggle').addEventListener('click', function () {
    var on = pInput.type === 'password';
    pInput.type = on ? 'text' : 'password';
    this.textContent = on ? 'Hide' : 'Show';
    this.setAttribute('aria-label', on ? 'Hide password' : 'Show password');
  });

  function setBusy(busy, targetBtn, text) {
    btnAD.disabled = busy;
    if (btnLocal) btnLocal.disabled = busy;
    if (targetBtn) {
      targetBtn.textContent = text;
    } else {
      btnAD.textContent = 'Sign in with AD BTN';
      if (btnLocal) btnLocal.textContent = 'Sign in with local account';
    }
  }

  function handleLogin(endpoint, isAD, targetBtn, loadingText) {
    var u = uInput.value.trim();
    var p = pInput.value;
    var totp = totpInput ? totpInput.value.trim() : '';

    if (!u || !p) {
      show('Enter your username and password.');
      return;
    }
    show('');
    setBusy(true, targetBtn, loadingText);

    var payload = { username: u, password: p, return_to: back };
    if (isAD && totp) payload.totp = totp;

    fetch(endpoint, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    })
      .then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (j) {
          return { status: r.status, j: j };
        });
      })
      .then(function (x) {
        if (x.status === 200 && x.j.redirect) {
          location.href = x.j.redirect;
          return;
        }
        setBusy(false);
        if (x.status === 429) {
          show('Too many attempts. Wait a few minutes and try again.');
        } else if (x.j && x.j.error === 'totp_required') {
          if (totpRow) {
            totpRow.hidden = false;
            totpInput.focus();
          }
          show('Enter your 6-digit authenticator code (TOTP).');
        } else if (x.status === 403) {
          show(x.j.error_description || 'Access denied: your account has no access to this application.');
        } else if (x.status === 401) {
          show('Invalid username or password.');
          pInput.select();
        } else {
          show('Sign-in is unavailable right now. Try again in a moment.');
        }
      })
      .catch(function () {
        setBusy(false);
        show('Could not reach the server. Check your connection.');
      });
  }

  // Submit form -> Sign in with AD BTN
  f.addEventListener('submit', function (e) {
    e.preventDefault();
    handleLogin('/login/ad', true, btnAD, 'Signing in with AD BTN…');
  });

  // Click local button -> Sign in with local account
  if (btnLocal) {
    btnLocal.addEventListener('click', function (e) {
      e.preventDefault();
      handleLogin('/login/local', false, btnLocal, 'Signing in with local account…');
    });
  }

  if (q.get('error') === 'sso') show('Single sign-on was not completed. Try again or use a local account.');
})();
