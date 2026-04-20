/**
 * Auth UI — session check, login, register, forgot/reset password, logout.
 *
 * Depends on: api-client.js (window.AuthExpiredError)
 *             api.js        (window.API)
 *
 * Exposes: window.Auth.checkSession
 */

'use strict';

(function () {

  const _mod = (typeof require !== 'undefined' && typeof module !== 'undefined')
    ? require('./api-client')
    : null;
  const AuthExpiredError = _mod ? _mod.AuthExpiredError : window.AuthExpiredError;
  const escapeHtml       = _mod ? _mod.escapeHtml       : window.escapeHtml;
  const TokenStore       = _mod ? _mod.TokenStore       : window.TokenStore;

  const _apiMod = (typeof require !== 'undefined' && typeof module !== 'undefined')
    ? require('./api')
    : null;
  const API = _apiMod ? _apiMod.API : window.API;

  // ---- Pure / testable functions ------------------------------------------

  /**
   * Pure function: derive a render descriptor from a session API response.
   * @param {{logged_in: boolean, user?: object}} data
   * @returns {{type: 'logged-in'|'logged-out', email?: string}}
   */
  function renderSessionState(data) {
    if (data.logged_in && data.user) {
      return { type: 'logged-in', email: data.user.email };
    }
    return { type: 'logged-out' };
  }

  // ---- DOM interaction ----------------------------------------------------
  // Only runs in browser/Electron. Skipped by Jest (no real document/window).

  if (typeof document !== 'undefined' && typeof window !== 'undefined' && !window._testMode) {

    // -- Session check -------------------------------------------------------

    async function checkSession() {
      const el = document.getElementById('session-status');
      el.className = 'card loading';
      el.innerHTML = '<p>Connecting to server…</p>';

      // On Electron, try to restore a persisted refresh token before hitting the server.
      // We must explicitly refresh here because GET /api/session returns 200 {logged_in:false}
      // for unauthenticated requests — never 401 — so the auto-refresh in APIClient.request
      // would never fire on its own.
      if (TokenStore._isElectron() && !TokenStore.getRefreshToken()) {
        try {
          const saved = await window.electronAPI.loadRefreshToken();
          if (saved) {
            TokenStore.store(null, saved);
            const ok = await APIClient.tryRefresh();
            if (!ok) window.electronAPI.clearRefreshToken();
          }
        } catch (e) {
          console.error('[remember-me] restore failed:', e);
        }
      }

      try {
        const resp = await API.fetchSession();
        if (!resp.ok) throw new Error(`Server responded with HTTP ${resp.status}`);
        const data = await resp.json();
        const state = renderSessionState(data);
        renderState(el, state);
      } catch (err) {
        if (err instanceof AuthExpiredError) {
          renderState(el, { type: 'logged-out' });
        } else {
          renderConnectionError(el);
        }
      }
    }

    function renderConnectionError(el) {
      el.className = 'card error';
      el.innerHTML = `
        <h2>Connection Error</h2>
        <p>Could not reach the server. Make sure the backend is running.</p>
      `;
      const btn = document.createElement('button');
      btn.textContent = 'Retry';
      btn.addEventListener('click', checkSession);
      el.appendChild(btn);
    }

    // -- State rendering -----------------------------------------------------

    function renderState(el, state) {
      if (state.type === 'logged-in') {
        el.className = 'card hidden';
        renderHeaderUser(state.email);
        window.Dashboard.show();
      } else {
        clearHeaderUser();
        window.Dashboard.hide();
        window.Files.hide();
        el.classList.remove('hidden');
        renderLoginForm(el);
      }
    }

    function renderHeaderUser(email) {
      const initials = email
        .split('@')[0]
        .replace(/[^a-zA-Z0-9]/g, ' ')
        .trim()
        .split(/\s+/)
        .slice(0, 2)
        .map(function (w) { return w[0] || ''; })
        .join('')
        .toUpperCase() || '?';

      const avatar = document.getElementById('header-avatar');
      if (avatar) { avatar.textContent = initials; avatar.setAttribute('title', email); }

      const emailEl = document.getElementById('header-email');
      if (emailEl) emailEl.textContent = email;

      const slot = document.getElementById('header-user');
      if (slot) slot.classList.remove('hidden');
    }

    function clearHeaderUser() {
      const slot = document.getElementById('header-user');
      if (slot) slot.classList.add('hidden');
    }

    // -- Login form ----------------------------------------------------------

    function renderLoginForm(el, errorMsg) {
      el.className = 'card logged-out';
      el.innerHTML = `
        <h2>Sign In</h2>
        <p>Connect to your Cloud Backup account.</p>
        <form id="login-form">
          <label>
            Email
            <input type="email" id="email" autocomplete="email" required />
          </label>
          <label>
            Password
            <div class="password-wrapper">
              <input type="password" id="password" autocomplete="current-password" required />
              <button type="button" class="password-toggle" aria-label="Show password" data-target="password">👁</button>
            </div>
          </label>
          <div id="remember-me-slot"></div>
          <div class="form-error" id="form-error">${errorMsg ? escapeHtml(errorMsg) : ''}</div>
          <button type="submit" id="login-submit-btn">Sign In</button>
          <button type="button" id="register-btn">Create Account</button>
          <button type="button" id="forgot-btn" class="link-btn">Forgot password?</button>
        </form>
      `;
      document.getElementById('login-form').addEventListener('submit', handleLogin);
      document.getElementById('register-btn').addEventListener('click', renderRegisterForm.bind(null, el));
      document.getElementById('forgot-btn').addEventListener('click', renderForgotPasswordForm.bind(null, el));
      el.querySelectorAll('.password-toggle').forEach(attachPasswordToggle);

      // Async: inject the remember-me checkbox only when safeStorage is confirmed available.
      // Runs after the form is already visible so callers need not be async.
      if (TokenStore._isElectron()) {
        window.electronAPI.isSafeStorageAvailable().then(function (available) {
          const slot = document.getElementById('remember-me-slot');
          if (!available || !slot) return;
          slot.innerHTML = `
            <label class="remember-me-label">
              <input type="checkbox" id="remember-me" />
              Remember me
            </label>`;
        }).catch(() => {});
      }
    }

    async function handleLogin(e) {
      e.preventDefault();
      const emailInput = document.getElementById('email');
      const passInput  = document.getElementById('password');
      const email      = emailInput.value.trim();
      const password   = passInput.value;
      const errorEl    = document.getElementById('form-error');
      const submitBtn  = document.getElementById('login-submit-btn');
      errorEl.textContent = '';
      clearFieldErrors([emailInput, passInput]);

      const rememberMe = document.getElementById('remember-me');

      setButtonLoading(submitBtn, true, 'Signing in…');
      try {
        const resp = await API.login(email, password);
        if (!resp.ok) {
          let msg = 'Login failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
          markFieldError(password ? passInput : emailInput);
          setButtonLoading(submitBtn, false, 'Sign In');
          return;
        }
        if (rememberMe && rememberMe.checked) {
          const refreshToken = TokenStore.getRefreshToken();
          if (refreshToken) {
            const result = await window.electronAPI.saveRefreshToken(refreshToken);
            if (result && result.error) console.error('[remember-me] save failed:', result.error);
          }
        }
        checkSession();
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
        setButtonLoading(submitBtn, false, 'Sign In');
      }
    }

    // -- Register form -------------------------------------------------------

    function renderRegisterForm(el) {
      el.className = 'card logged-out';
      el.innerHTML = `
        <h2>Create Account</h2>
        <form id="register-form">
          <label>
            Email
            <input type="email" id="reg-email" required />
          </label>
          <label>
            Password
            <div class="password-wrapper">
              <input type="password" id="reg-password" required />
              <button type="button" class="password-toggle" aria-label="Show password" data-target="reg-password">👁</button>
            </div>
          </label>
          <div class="form-error" id="reg-error"></div>
          <button type="submit" id="reg-submit-btn">Register</button>
          <button type="button" id="back-btn">Back to Sign In</button>
        </form>
      `;
      document.getElementById('register-form').addEventListener('submit', handleRegister);
      document.getElementById('back-btn').addEventListener('click', () => renderLoginForm(el));
      el.querySelectorAll('.password-toggle').forEach(attachPasswordToggle);
    }

    async function handleRegister(e) {
      e.preventDefault();
      const emailInput = document.getElementById('reg-email');
      const passInput  = document.getElementById('reg-password');
      const email      = emailInput.value.trim();
      const password   = passInput.value;
      const errorEl    = document.getElementById('reg-error');
      const submitBtn  = document.getElementById('reg-submit-btn');
      errorEl.textContent = '';
      clearFieldErrors([emailInput, passInput]);

      setButtonLoading(submitBtn, true, 'Creating account…');
      try {
        const resp = await API.register(email, password);
        if (!resp.ok) {
          let msg = 'Registration failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
          markFieldError(emailInput);
          setButtonLoading(submitBtn, false, 'Register');
          return;
        }
        // Auth cookies are set by the server; nothing to store in JS.
        checkSession();
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
        setButtonLoading(submitBtn, false, 'Register');
      }
    }

    // -- Forgot password flow ------------------------------------------------

    function renderForgotPasswordForm(el) {
      el.className = 'card logged-out';
      el.innerHTML = `
        <h2>Forgot Password</h2>
        <p>Enter your email to receive a reset token.</p>
        <form id="forgot-form">
          <label>Email <input type="email" id="fp-email" required /></label>
          <div class="form-error" id="fp-error"></div>
          <button type="submit" id="fp-submit-btn">Send Reset Token</button>
          <button type="button" id="back-btn">Back to Sign In</button>
        </form>
      `;
      document.getElementById('forgot-form').addEventListener('submit', handleForgotPassword.bind(null, el));
      document.getElementById('back-btn').addEventListener('click', () => renderLoginForm(el));
    }

    async function handleForgotPassword(el, e) {
      e.preventDefault();
      const emailInput = document.getElementById('fp-email');
      const email      = emailInput.value.trim();
      const errorEl    = document.getElementById('fp-error');
      const submitBtn  = document.getElementById('fp-submit-btn');
      errorEl.textContent = '';
      clearFieldErrors([emailInput]);

      setButtonLoading(submitBtn, true, 'Sending…');
      try {
        const resp = await API.forgotPassword(email);
        if (!resp.ok) {
          let msg = 'Request failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
          markFieldError(emailInput);
          setButtonLoading(submitBtn, false, 'Send Reset Token');
          return;
        }
        const data = await resp.json();
        renderResetPasswordForm(el, data.reset_token || '');
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
        setButtonLoading(submitBtn, false, 'Send Reset Token');
      }
    }

    function renderResetPasswordForm(el, prefillToken) {
      el.className = 'card logged-out';
      el.innerHTML = `
        <h2>Reset Password</h2>
        ${prefillToken
          ? `<p class="dev-note">Dev mode: reset token pre-filled below.<br>In production this would arrive by email.</p>`
          : '<p>Enter the reset token from your email and choose a new password.</p>'
        }
        <form id="reset-form">
          <label>
            Reset Token
            <input type="text" id="reset-token" value="${escapeHtml(prefillToken)}" required />
          </label>
          <label>
            New Password
            <div class="password-wrapper">
              <input type="password" id="new-password" required />
              <button type="button" class="password-toggle" aria-label="Show password" data-target="new-password">👁</button>
            </div>
          </label>
          <label>
            Confirm Password
            <div class="password-wrapper">
              <input type="password" id="confirm-password" required />
              <button type="button" class="password-toggle" aria-label="Show password" data-target="confirm-password">👁</button>
            </div>
          </label>
          <div class="form-error" id="reset-error"></div>
          <button type="submit" id="reset-submit-btn">Reset Password</button>
          <button type="button" id="back-btn">Back to Sign In</button>
        </form>
      `;
      document.getElementById('reset-form').addEventListener('submit', handleResetPassword.bind(null, el));
      document.getElementById('back-btn').addEventListener('click', () => renderLoginForm(el));
      el.querySelectorAll('.password-toggle').forEach(attachPasswordToggle);
    }

    async function handleResetPassword(el, e) {
      e.preventDefault();
      const newPassInput  = document.getElementById('new-password');
      const confPassInput = document.getElementById('confirm-password');
      const resetToken    = document.getElementById('reset-token').value.trim();
      const newPassword   = newPassInput.value;
      const confirmPass   = confPassInput.value;
      const errorEl       = document.getElementById('reset-error');
      const submitBtn     = document.getElementById('reset-submit-btn');
      errorEl.textContent = '';
      clearFieldErrors([newPassInput, confPassInput]);

      if (newPassword !== confirmPass) {
        errorEl.textContent = 'Passwords do not match';
        markFieldError(confPassInput);
        return;
      }

      setButtonLoading(submitBtn, true, 'Resetting…');
      try {
        const resp = await API.resetPassword(resetToken, newPassword);
        if (!resp.ok) {
          let msg = 'Reset failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
          markFieldError(newPassInput);
          setButtonLoading(submitBtn, false, 'Reset Password');
          return;
        }
        renderLoginForm(el, 'Password updated. Please sign in with your new password.');
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
        setButtonLoading(submitBtn, false, 'Reset Password');
      }
    }

    // -- Auth form helpers ----------------------------------------------------

    function setButtonLoading(btn, loading, label) {
      if (!btn) return;
      btn.disabled = loading;
      btn.innerHTML = loading
        ? '<span class="btn-spinner" aria-hidden="true"></span>' + escapeHtml(label)
        : escapeHtml(label);
    }

    function markFieldError(input) {
      if (!input) return;
      input.classList.add('input-error');
      input.classList.remove('input-shake');
      // Re-trigger shake animation
      void input.offsetWidth;
      input.classList.add('input-shake');
      input.addEventListener('animationend', function () {
        input.classList.remove('input-shake');
      }, { once: true });
    }

    function clearFieldErrors(inputs) {
      for (const input of inputs) {
        if (input) input.classList.remove('input-error', 'input-shake');
      }
    }

    function attachPasswordToggle(btn) {
      btn.addEventListener('click', function () {
        const targetId = btn.getAttribute('data-target');
        const input = document.getElementById(targetId);
        if (!input) return;
        const showing = input.type === 'text';
        input.type = showing ? 'password' : 'text';
        btn.setAttribute('aria-label', showing ? 'Show password' : 'Hide password');
        btn.textContent = showing ? '👁' : '🙈';
      });
    }

    // -- Logout --------------------------------------------------------------

    async function logout() {
      try {
        await API.logout();
      } catch {
        // Ignore network errors — token will expire naturally.
      }
      TokenStore.clear();
      checkSession();
    }

    // -- Expose public interface ---------------------------------------------

    // Wire up the static logout button (always present in index.html).
    const _staticLogoutBtn = document.getElementById('logout-btn');
    if (_staticLogoutBtn) _staticLogoutBtn.addEventListener('click', logout);

    // Wire up Account and Settings nav buttons.
    document.getElementById('account-nav-btn').addEventListener('click', function () {
      const email = document.getElementById('header-email').textContent;
      window.Account.show(email);
    });
    document.getElementById('settings-nav-btn').addEventListener('click', function () {
      window.Settings.show();
    });

    // -- #23 Theme toggle ----------------------------------------------------

    (function initThemeToggle() {
      // Load saved preference
      const saved = localStorage.getItem('theme');
      if (saved === 'light') {
        document.documentElement.setAttribute('data-theme', 'light');
      }

      // Inject toggle button into the header, before #header-user
      const headerUser = document.getElementById('header-user');
      if (headerUser) {
        const btn = document.createElement('button');
        btn.id = 'theme-toggle-btn';
        btn.className = 'theme-toggle-btn';
        btn.setAttribute('type', 'button');
        btn.setAttribute('aria-label', 'Toggle light/dark theme');
        btn.textContent = document.documentElement.getAttribute('data-theme') === 'light' ? '☀️' : '🌙';
        // Insert before header-user in the header
        headerUser.parentNode.insertBefore(btn, headerUser);

        btn.addEventListener('click', function () {
          const isLight = document.documentElement.getAttribute('data-theme') === 'light';
          if (isLight) {
            document.documentElement.removeAttribute('data-theme');
            localStorage.setItem('theme', 'dark');
            btn.textContent = '🌙';
          } else {
            document.documentElement.setAttribute('data-theme', 'light');
            localStorage.setItem('theme', 'light');
            btn.textContent = '☀️';
          }
        });
      }
    }());

    window.Auth = { checkSession };
  }

  // ---- Exports (for Jest) -------------------------------------------------

  if (typeof module !== 'undefined') {
    module.exports = { renderSessionState };
  }

})();
