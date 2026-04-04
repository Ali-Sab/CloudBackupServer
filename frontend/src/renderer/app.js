/**
 * Cloud Backup renderer — session UI, login, register, logout, forgot/reset password.
 *
 * All API calls go through APIClient (api-client.js) which handles:
 *   - Authorization header injection
 *   - Automatic access-token refresh on 401
 *   - AuthExpiredError when the refresh token is also expired
 */

'use strict';

// Load api-client (Node require when in Jest; script tag already loaded in browser).
const _apiClientModule = (typeof require !== 'undefined' && typeof module !== 'undefined')
  ? require('./api-client')
  : null;

const APIClient   = _apiClientModule ? _apiClientModule.APIClient   : window.APIClient;
const TokenStore  = _apiClientModule ? _apiClientModule.TokenStore  : window.TokenStore;
const AuthExpiredError = _apiClientModule ? _apiClientModule.AuthExpiredError : window.AuthExpiredError;

// ---- Pure / testable functions ------------------------------------------

/**
 * Fetch the current session state from the backend.
 * @returns {Promise<{logged_in: boolean, user?: object}>}
 */
async function fetchSession() {
  const resp = await APIClient.request('/api/session');
  if (!resp.ok) throw new Error(`Server responded with HTTP ${resp.status}`);
  return resp.json();
}

/**
 * Pure function: derive a render descriptor from a session API response.
 * @param {{logged_in: boolean, user?: object}} data
 * @returns {{type: 'logged-in'|'logged-out', username?: string, email?: string}}
 */
function renderSessionState(data) {
  if (data.logged_in && data.user) {
    return { type: 'logged-in', username: data.user.username, email: data.user.email };
  }
  return { type: 'logged-out' };
}

/** XSS-safe HTML escaping. */
function escapeHtml(str) {
  const div = document.createElement('div');
  div.appendChild(document.createTextNode(String(str)));
  return div.innerHTML;
}

// ---- DOM interaction ----------------------------------------------------
// Only runs in browser/Electron. Skipped by Jest (no real document/window).

if (typeof document !== 'undefined' && typeof window !== 'undefined' && !window._testMode) {

  // -- Session check -------------------------------------------------------

  async function checkSession() {
    const el = document.getElementById('session-status');
    el.className = 'card loading';
    el.innerHTML = '<p>Connecting to server…</p>';

    try {
      const data = await fetchSession();
      const state = renderSessionState(data);
      renderState(el, state);
    } catch (err) {
      if (err instanceof AuthExpiredError) {
        // Refresh token is also gone — show login form
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
      <button onclick="checkSession()">Retry</button>
    `;
  }

  // -- State rendering -----------------------------------------------------

  function renderState(el, state) {
    if (state.type === 'logged-in') {
      el.className = 'card logged-in';
      el.innerHTML = `
        <h2>Welcome, ${escapeHtml(state.username)}</h2>
        <p>Signed in as <strong>${escapeHtml(state.email)}</strong></p>
        <button id="logout-btn">Sign Out</button>
      `;
      document.getElementById('logout-btn').addEventListener('click', logout);
    } else {
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
          Username
          <input type="text" id="username" autocomplete="username" required />
        </label>
        <label>
          Password
          <input type="password" id="password" autocomplete="current-password" required />
        </label>
        <div class="form-error" id="form-error">${errorMsg ? escapeHtml(errorMsg) : ''}</div>
        <button type="submit">Sign In</button>
        <button type="button" id="register-btn">Create Account</button>
        <button type="button" id="forgot-btn" class="link-btn">Forgot password?</button>
      </form>
    `;
    document.getElementById('login-form').addEventListener('submit', handleLogin);
    document.getElementById('register-btn').addEventListener('click', renderRegisterForm.bind(null, el));
    document.getElementById('forgot-btn').addEventListener('click', renderForgotPasswordForm.bind(null, el));
  }

  async function handleLogin(e) {
    e.preventDefault();
    const username = document.getElementById('username').value.trim();
    const password = document.getElementById('password').value;
    const errorEl  = document.getElementById('form-error');
    errorEl.textContent = '';

    try {
      const resp = await APIClient.post('/api/auth/login', { username, password });
      const data = await resp.json();
      if (!resp.ok) {
        errorEl.textContent = data.error || 'Login failed';
        return;
      }
      TokenStore.store(data.access_token, data.refresh_token);
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
        <label>Username <input type="text" id="reg-username" required /></label>
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
    const body = {
      username: document.getElementById('reg-username').value.trim(),
      email:    document.getElementById('reg-email').value.trim(),
      password: document.getElementById('reg-password').value,
    };
    const errorEl = document.getElementById('reg-error');
    errorEl.textContent = '';

    try {
      const resp = await APIClient.post('/api/auth/register', body);
      const data = await resp.json();
      if (!resp.ok) {
        errorEl.textContent = data.error || 'Registration failed';
        return;
      }
      TokenStore.store(data.access_token, data.refresh_token);
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
      <p>Enter your username to receive a reset token.</p>
      <form id="forgot-form">
        <label>Username <input type="text" id="fp-username" required /></label>
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
    const username = document.getElementById('fp-username').value.trim();
    const errorEl  = document.getElementById('fp-error');
    errorEl.textContent = '';

    try {
      const resp = await APIClient.post('/api/auth/forgot-password', { username });
      const data = await resp.json();
      if (!resp.ok) {
        errorEl.textContent = data.error || 'Request failed';
        return;
      }
      // Show the reset-password form, pre-filling the token if the server returned one (dev mode).
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
    const resetToken    = document.getElementById('reset-token').value.trim();
    const newPassword   = document.getElementById('new-password').value;
    const confirmPass   = document.getElementById('confirm-password').value;
    const errorEl       = document.getElementById('reset-error');
    errorEl.textContent = '';

    if (newPassword !== confirmPass) {
      errorEl.textContent = 'Passwords do not match';
      return;
    }

    try {
      const resp = await APIClient.post('/api/auth/reset-password', {
        reset_token: resetToken,
        new_password: newPassword,
      });
      const data = await resp.json();
      if (!resp.ok) {
        errorEl.textContent = data.error || 'Reset failed';
        return;
      }
      // Password changed — clear any stale session and show login
      TokenStore.clear();
      renderLoginForm(el, 'Password updated. Please sign in with your new password.');
    } catch {
      errorEl.textContent = 'Connection error — please try again.';
    }
  }

  // -- Logout --------------------------------------------------------------

  async function logout() {
    const refreshToken = TokenStore.getRefreshToken();
    // Best-effort server-side revocation — clear locally regardless of outcome.
    if (refreshToken) {
      try {
        await APIClient.post('/api/auth/logout', { refresh_token: refreshToken });
      } catch {
        // Ignore network errors — local clear still happens below.
      }
    }
    TokenStore.clear();
    checkSession();
  }

  // -- Boot ----------------------------------------------------------------

  document.addEventListener('DOMContentLoaded', checkSession);
  window.checkSession = checkSession; // used by the retry button inline onclick
}

// ---- Exports (for Jest) -------------------------------------------------

if (typeof module !== 'undefined') {
  module.exports = { fetchSession, renderSessionState, escapeHtml };
}
