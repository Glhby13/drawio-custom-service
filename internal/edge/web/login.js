(function () {
  'use strict';
  var f = document.getElementById('f');
  var err = document.getElementById('err');
  var errText = document.getElementById('err-text');
  var errClose = document.getElementById('err-close');
  var btnAD = document.getElementById('submit');
  var btnLocal = document.getElementById('local-btn');
  var totpRow = document.getElementById('totp-row');
  var totpInput = document.getElementById('totp');
  var uInput = document.getElementById('username');
  var pInput = document.getElementById('password');
  var ssoLink = document.getElementById('sso');
  var themeToggle = document.getElementById('theme-toggle');
  var themeIcon = document.getElementById('theme-icon');
  var brandLogo = document.getElementById('brand-logo');

  var q = new URLSearchParams(location.search);
  var back = q.get('return_to') || '/';
  if (back.charAt(0) !== '/' || back.slice(0, 2) === '//') back = '/';
  if (ssoLink) ssoLink.href = '/login/sso?return_to=' + encodeURIComponent(back);

  // Random day/night mode on login page (not persisted to localStorage)
  var loginTheme = Math.random() < 0.5 ? 'dark' : 'light';
  var themeTransTimer = null;
  function makeThemeSvg(isDark) {
    var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('width', '17');
    svg.setAttribute('height', '17');
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', '2');
    svg.setAttribute('stroke-linecap', 'round');
    svg.setAttribute('stroke-linejoin', 'round');
    if (isDark) {
      // Lucide sun icon (shown in night mode to switch to day mode)
      var c = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
      c.setAttribute('cx', '12'); c.setAttribute('cy', '12'); c.setAttribute('r', '4');
      svg.appendChild(c);
      ['M12 2v2', 'M12 20v2', 'm4.93 4.93 1.41 1.41', 'm17.66 17.66 1.41 1.41', 'M2 12h2', 'M20 12h2', 'm6.34 17.66-1.41 1.41', 'm19.07 4.93-1.41 1.41'].forEach(function (d) {
        var p = document.createElementNS('http://www.w3.org/2000/svg', 'path');
        p.setAttribute('d', d);
        svg.appendChild(p);
      });
    } else {
      // Lucide moon icon (shown in day mode to switch to night mode)
      var p = document.createElementNS('http://www.w3.org/2000/svg', 'path');
      p.setAttribute('d', 'M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z');
      svg.appendChild(p);
    }
    return svg;
  }
  function applyLoginTheme(mode, animate) {
    loginTheme = mode === 'dark' ? 'dark' : 'light';
    var root = document.documentElement;
    if (animate) {
      clearTimeout(themeTransTimer);
      root.classList.add('theme-transition');
      themeTransTimer = setTimeout(function () {
        root.classList.remove('theme-transition');
      }, 400);
    }
    root.setAttribute('data-theme', loginTheme);
    if (brandLogo) {
      var nextSrc = loginTheme === 'dark' ? '/login-assets/drawspace-logo-dark.svg' : '/login-assets/drawspace-logo.svg';
      if (animate && brandLogo.getAttribute('src') !== nextSrc) {
        brandLogo.style.opacity = '0.35';
        setTimeout(function () {
          brandLogo.src = nextSrc;
          brandLogo.style.opacity = '1';
        }, 120);
      } else {
        brandLogo.src = nextSrc;
      }
    }
    if (themeIcon) {
      themeIcon.textContent = '';
      if (animate) {
        themeIcon.classList.remove('pop');
        void themeIcon.offsetWidth;
        themeIcon.classList.add('pop');
      }
      themeIcon.appendChild(makeThemeSvg(loginTheme === 'dark'));
    }
    if (themeToggle) {
      var tip = loginTheme === 'dark' ? 'Switch to day mode' : 'Switch to night mode';
      themeToggle.setAttribute('title', tip);
      themeToggle.setAttribute('aria-label', tip);
    }
  }
  applyLoginTheme(loginTheme, false);
  if (themeToggle) {
    themeToggle.addEventListener('click', function () {
      applyLoginTheme(loginTheme === 'dark' ? 'light' : 'dark', true);
    });
  }

  var errTimer = null;
  function show(msg) {
    clearTimeout(errTimer);
    if (errText) errText.textContent = msg;
    else err.textContent = msg;
    err.hidden = !msg;
    if (msg) {
      errTimer = setTimeout(function () {
        show('');
      }, 3000);
    }
  }

  if (errClose) {
    errClose.addEventListener('click', function (e) {
      e.stopPropagation();
      show('');
    });
  }
  document.addEventListener('mousedown', function (e) {
    if (!err.hidden && !err.contains(e.target)) {
      show('');
    }
  });

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
