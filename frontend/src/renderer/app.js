/**
 * Cloud Backup renderer logic.
 *
 * Exported as `CloudBackup` so Jest can require() this module in tests
 * without needing a real browser or Electron runtime.
 */

const API_BASE_URL = (typeof process !== 'undefined' && process.env.API_BASE_URL)
  ? process.env.API_BASE_URL
  : 'http://localhost:8080';

const CloudBackup = {
  API_BASE_URL,

  /**
   * Fetch the current session state from the backend.
   * @param {string|null} token - JWT bearer token, or null if unauthenticated.
   * @returns {Promise<{logged_in: boolean, user?: object}>}
   */
  async fetchSession(token) {
    const headers = {};
    if (token) {
      headers['Authorization'] = `Bearer ${token}`;
    }
    const response = await fetch(`${this.API_BASE_URL}/api/session`, { headers });
    if (!response.ok) {
      throw new Error(`Server responded with HTTP ${response.status}`);
    }
    return response.json();
  },

  /**
   * Pure function: derive a render descriptor from a session API response.
   * @param {{logged_in: boolean, user?: object}} data
   * @returns {{type: 'logged-in'|'logged-out', username?: string, email?: string}}
   */
  renderSessionState(data) {
    if (data.logged_in && data.user) {
      return { type: 'logged-in', username: data.user.username, email: data.user.email };
    }
    return { type: 'logged-out' };
  },

  /** XSS-safe HTML escaping. */
  escapeHtml(str) {
    const div = document.createElement('div');
    div.appendChild(document.createTextNode(String(str)));
    return div.innerHTML;
  },
};

// ---- DOM interaction (only runs in browser/Electron, not in Jest) ----

if (typeof document !== 'undefined' && typeof window !== 'undefined' && !window._testMode) {
  /**
   * Retrieve the auth token from Electron main process (if available) or localStorage.
   */
  function getStoredToken() {
    if (window.electronAPI) return window.electronAPI.getToken();
    return localStorage.getItem('auth_token');
  }

  function storeToken(token) {
    if (window.electronAPI) window.electronAPI.setToken(token);
    localStorage.setItem('auth_token', token);
  }

  function clearStoredToken() {
    if (window.electronAPI) window.electronAPI.clearToken();
    localStorage.removeItem('auth_token');
  }

  async function checkSession() {
    const el = document.getElementById('session-status');
    el.className = 'card loading';
    el.innerHTML = '<p>Connecting to server…</p>';

    try {
      const token = getStoredToken();
      const data = await CloudBackup.fetchSession(token);
      const state = CloudBackup.renderSessionState(data);
      renderState(el, state);
    } catch (err) {
      el.className = 'card error';
      el.innerHTML = `
        <h2>Connection Error</h2>
        <p>Could not reach the server. Make sure the backend is running.</p>
        <button onclick="checkSession()">Retry</button>
      `;
    }
  }

  function renderState(el, state) {
    if (state.type === 'logged-in') {
      el.className = 'card logged-in';
      el.innerHTML = `
        <h2>Welcome, ${CloudBackup.escapeHtml(state.username)}</h2>
        <p>Signed in as <strong>${CloudBackup.escapeHtml(state.email)}</strong></p>
        <button id="logout-btn">Sign Out</button>
      `;
      document.getElementById('logout-btn').addEventListener('click', logout);
    } else {
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
          <div class="form-error" id="form-error"></div>
          <button type="submit">Sign In</button>
          <button type="button" id="register-btn">Create Account</button>
        </form>
      `;
      document.getElementById('login-form').addEventListener('submit', handleLogin);
      document.getElementById('register-btn').addEventListener('click', handleRegister);
    }
  }

  async function handleLogin(e) {
    e.preventDefault();
    const username = document.getElementById('username').value.trim();
    const password = document.getElementById('password').value;
    const errorEl = document.getElementById('form-error');
    errorEl.textContent = '';

    try {
      const resp = await fetch(`${CloudBackup.API_BASE_URL}/api/auth/login`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      });
      const data = await resp.json();
      if (!resp.ok) {
        errorEl.textContent = data.error || 'Login failed';
        return;
      }
      storeToken(data.token);
      checkSession();
    } catch {
      errorEl.textContent = 'Connection error — please try again.';
    }
  }

  async function handleRegister() {
    const el = document.getElementById('session-status');
    el.className = 'card logged-out';
    el.innerHTML = `
      <h2>Create Account</h2>
      <form id="register-form">
        <label>Username <input type="text" id="reg-username" required /></label>
        <label>Email <input type="email" id="reg-email" required /></label>
        <label>Password <input type="password" id="reg-password" required /></label>
        <div class="form-error" id="reg-error"></div>
        <button type="submit">Register</button>
        <button type="button" id="back-btn">Back to Sign In</button>
      </form>
    `;
    document.getElementById('register-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const body = {
        username: document.getElementById('reg-username').value.trim(),
        email: document.getElementById('reg-email').value.trim(),
        password: document.getElementById('reg-password').value,
      };
      const errorEl = document.getElementById('reg-error');
      try {
        const resp = await fetch(`${CloudBackup.API_BASE_URL}/api/auth/register`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        const data = await resp.json();
        if (!resp.ok) {
          errorEl.textContent = data.error || 'Registration failed';
          return;
        }
        storeToken(data.token);
        checkSession();
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
      }
    });
    document.getElementById('back-btn').addEventListener('click', checkSession);
  }

  function logout() {
    clearStoredToken();
    checkSession();
  }

  // Boot
  document.addEventListener('DOMContentLoaded', checkSession);
  // Expose for inline onclick on retry button
  window.checkSession = checkSession;
}

// Export for Jest
if (typeof module !== 'undefined') {
  module.exports = CloudBackup;
}
