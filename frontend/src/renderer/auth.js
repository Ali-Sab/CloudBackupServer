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
        el.className = 'card logged-in';
        el.innerHTML = `
          <h2>Welcome back</h2>
          <p>Signed in as <strong>${escapeHtml(state.email)}</strong></p>
          <button id="logout-btn">Sign Out</button>
        `;
        document.getElementById('logout-btn').addEventListener('click', logout);
        window.Dashboard.show();
      } else {
        window.Dashboard.hide();
        window.Files.hide();
        renderLoginForm(el);
      }
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
            <input type="password" id="password" autocomplete="current-password" required />
          </label>
          <div id="remember-me-slot"></div>
          <div class="form-error" id="form-error">${errorMsg ? escapeHtml(errorMsg) : ''}</div>
          <button type="submit">Sign In</button>
          <button type="button" id="register-btn">Create Account</button>
          <button type="button" id="forgot-btn" class="link-btn">Forgot password?</button>
        </form>
      `;
      document.getElementById('login-form').addEventListener('submit', handleLogin);
      document.getElementById('register-btn').addEventListener('click', renderRegisterForm.bind(null, el));
      document.getElementById('forgot-btn').addEventListener('click', renderForgotPasswordForm.bind(null, el));

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
      const email    = document.getElementById('email').value.trim();
      const password = document.getElementById('password').value;
      const errorEl  = document.getElementById('form-error');
      errorEl.textContent = '';

      const rememberMe = document.getElementById('remember-me');

      try {
        const resp = await API.login(email, password);
        if (!resp.ok) {
          let msg = 'Login failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
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
      }
    }

    // -- Register form -------------------------------------------------------

    function renderRegisterForm(el) {
      el.className = 'card logged-out';
      el.innerHTML = `
        <h2>Create Account</h2>
        <form id="register-form">
          <label>Email    <input type="email" id="reg-email" required /></label>
          <label>Password <input type="password" id="reg-password" required /></label>
          <div class="form-error" id="reg-error"></div>
          <button type="submit">Register</button>
          <button type="button" id="back-btn">Back to Sign In</button>
        </form>
      `;
      document.getElementById('register-form').addEventListener('submit', handleRegister);
      document.getElementById('back-btn').addEventListener('click', () => renderLoginForm(el));
    }

    async function handleRegister(e) {
      e.preventDefault();
      const email    = document.getElementById('reg-email').value.trim();
      const password = document.getElementById('reg-password').value;
      const errorEl  = document.getElementById('reg-error');
      errorEl.textContent = '';

      try {
        const resp = await API.register(email, password);
        if (!resp.ok) {
          let msg = 'Registration failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
          return;
        }
        // Auth cookies are set by the server; nothing to store in JS.
        checkSession();
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
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
          <button type="submit">Send Reset Token</button>
          <button type="button" id="back-btn">Back to Sign In</button>
        </form>
      `;
      document.getElementById('forgot-form').addEventListener('submit', handleForgotPassword.bind(null, el));
      document.getElementById('back-btn').addEventListener('click', () => renderLoginForm(el));
    }

    async function handleForgotPassword(el, e) {
      e.preventDefault();
      const email   = document.getElementById('fp-email').value.trim();
      const errorEl = document.getElementById('fp-error');
      errorEl.textContent = '';

      try {
        const resp = await API.forgotPassword(email);
        if (!resp.ok) {
          let msg = 'Request failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
          return;
        }
        const data = await resp.json();
        renderResetPasswordForm(el, data.reset_token || '');
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
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
          <label>New Password <input type="password" id="new-password" required /></label>
          <label>Confirm Password <input type="password" id="confirm-password" required /></label>
          <div class="form-error" id="reset-error"></div>
          <button type="submit">Reset Password</button>
          <button type="button" id="back-btn">Back to Sign In</button>
        </form>
      `;
      document.getElementById('reset-form').addEventListener('submit', handleResetPassword.bind(null, el));
      document.getElementById('back-btn').addEventListener('click', () => renderLoginForm(el));
    }

    async function handleResetPassword(el, e) {
      e.preventDefault();
      const resetToken  = document.getElementById('reset-token').value.trim();
      const newPassword = document.getElementById('new-password').value;
      const confirmPass = document.getElementById('confirm-password').value;
      const errorEl     = document.getElementById('reset-error');
      errorEl.textContent = '';

      if (newPassword !== confirmPass) {
        errorEl.textContent = 'Passwords do not match';
        return;
      }

      try {
        const resp = await API.resetPassword(resetToken, newPassword);
        if (!resp.ok) {
          let msg = 'Reset failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
          return;
        }
        renderLoginForm(el, 'Password updated. Please sign in with your new password.');
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
      }
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

    window.Auth = { checkSession };
  }

  // ---- Exports (for Jest) -------------------------------------------------

  if (typeof module !== 'undefined') {
    module.exports = { renderSessionState };
  }

})();
