/**
 * API client for Cloud Backup Server.
 *
 * Provides:
 *   - TokenStore  — centralised read/write for access + refresh tokens
 *   - APIClient   — fetch wrapper that auto-refreshes on 401
 *   - AuthExpiredError — thrown when a refresh attempt fails
 *   - escapeHtml      — XSS-safe HTML escaping (shared utility)
 */

'use strict';

const BASE_URL = (typeof process !== 'undefined' && process.env.API_BASE_URL)
  ? process.env.API_BASE_URL
  : 'http://localhost:8080';

// ---- Utilities -----------------------------------------------------------

/** XSS-safe HTML escaping. Canonical definition — shared by auth.js and files.js. */
function escapeHtml(str) {
  const div = document.createElement('div');
  div.appendChild(document.createTextNode(String(str)));
  return div.innerHTML;
}

// ---- Token storage -------------------------------------------------------

/**
 * TokenStore abstracts over in-memory storage (Electron) and
 * localStorage (fallback for tests / browser contexts).
 *
 * In Electron, fetch requests originate from file:// so cookies set by the
 * http://localhost backend are never attached (cross-scheme). We store tokens
 * in memory instead and send them as Authorization: Bearer headers.
 *
 * Access and refresh tokens are always updated together to stay in sync.
 */
const _mem = { accessToken: null, refreshToken: null };

const TokenStore = {
  _isElectron() {
    return typeof window !== 'undefined' && !!window.electronAPI;
  },

  getAccessToken() {
    if (this._isElectron()) return _mem.accessToken;
    return (typeof localStorage !== 'undefined') ? localStorage.getItem('access_token') : null;
  },

  getRefreshToken() {
    if (this._isElectron()) return _mem.refreshToken;
    return (typeof localStorage !== 'undefined') ? localStorage.getItem('refresh_token') : null;
  },

  store(accessToken, refreshToken) {
    if (this._isElectron()) {
      _mem.accessToken = accessToken;
      _mem.refreshToken = refreshToken;
      // Keep the keychain in sync on token rotation: if a persisted token already
      // exists (remember-me was opted in), overwrite it with the new one so the
      // next restart uses a valid token rather than the stale original.
      if (refreshToken) {
        const tokenSnapshot = refreshToken;
        window.electronAPI.loadRefreshToken().then(existing => {
          // Only write if: (a) remember-me is active (file exists), and
          // (b) the token hasn't been superseded by a logout or another rotation.
          if (existing && TokenStore.getRefreshToken() === tokenSnapshot) {
            return window.electronAPI.saveRefreshToken(tokenSnapshot);
          }
        }).catch(() => {});
      }
      return;
    }
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem('access_token', accessToken);
      localStorage.setItem('refresh_token', refreshToken);
    }
  },

  clear() {
    if (this._isElectron()) {
      _mem.accessToken = null;
      _mem.refreshToken = null;
      window.electronAPI.clearRefreshToken();
      return;
    }
    if (typeof localStorage !== 'undefined') {
      localStorage.removeItem('access_token');
      localStorage.removeItem('refresh_token');
    }
  },
};

// ---- Auth error ----------------------------------------------------------

class AuthExpiredError extends Error {
  constructor() {
    super('Session expired — please log in again');
    this.name = 'AuthExpiredError';
  }
}

// ---- Refresh lock --------------------------------------------------------
// Ensures that concurrent 401 responses share a single refresh call rather
// than hammering the /api/auth/refresh endpoint in parallel.

let _refreshPromise = null;

async function _doRefresh() {
  const refreshToken = TokenStore.getRefreshToken();
  if (!refreshToken) return false;

  try {
    const resp = await fetch(`${BASE_URL}/api/auth/refresh`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: refreshToken }),
    });

    if (!resp.ok) {
      TokenStore.clear();
      return false;
    }

    const data = await resp.json();
    TokenStore.store(data.access_token, data.refresh_token);
    return true;
  } catch {
    TokenStore.clear();
    return false;
  }
}

function _refreshOnce() {
  if (!_refreshPromise) {
    _refreshPromise = _doRefresh().finally(() => { _refreshPromise = null; });
  }
  return _refreshPromise;
}

// ---- APIClient -----------------------------------------------------------

const APIClient = {
  BASE_URL,

  /**
   * Make an authenticated request.
   *
   * Injects `Authorization: Bearer <access_token>` if an access token exists.
   * On a 401 response, attempts one token refresh then retries the request.
   * If the refresh fails, clears all tokens and throws AuthExpiredError.
   *
   * @param {string} path  - e.g. "/api/session"
   * @param {RequestInit} [options]
   * @returns {Promise<Response>}
   */
  async request(path, options = {}) {
    let resp = await this._doRequest(path, options);

    if (resp.status === 401 && TokenStore.getRefreshToken()) {
      const refreshed = await _refreshOnce();
      if (!refreshed) throw new AuthExpiredError();
      resp = await this._doRequest(path, options);
    }

    return resp;
  },

  /**
   * Make an unauthenticated POST (login, register, forgot-password, etc.).
   *
   * @param {string} path
   * @param {object} body - will be JSON-serialised
   * @returns {Promise<Response>}
   */
  async post(path, body) {
    const resp = await fetch(`${BASE_URL}${path}`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    // In Electron, cookies don't cross the file:// → http:// boundary, so we
    // capture tokens from the response body and store them for Bearer auth.
    if (resp.ok && TokenStore._isElectron()) {
      try {
        const data = await resp.clone().json();
        if (data.access_token && data.refresh_token) {
          TokenStore.store(data.access_token, data.refresh_token);
        }
      } catch {}
    }
    return resp;
  },

  /**
   * Make an authenticated PUT with a JSON body.
   *
   * @param {string} path
   * @param {object} body - will be JSON-serialised
   * @returns {Promise<Response>}
   */
  async put(path, body) {
    return this.request(path, {
      method: 'PUT',
      body: JSON.stringify(body),
    });
  },

  /**
   * Attempt to exchange the current refresh token for a new access token.
   * Returns true if successful, false if the refresh token is absent or rejected.
   * Used by checkSession on startup to restore a persisted "remember me" session
   * before hitting endpoints that return 200/logged_in:false rather than 401.
   */
  tryRefresh() {
    if (!TokenStore.getRefreshToken()) return Promise.resolve(false);
    return _refreshOnce();
  },

  // Internal: raw request with Authorization header injected.
  async _doRequest(path, options = {}) {
    const accessToken = TokenStore.getAccessToken();
    const headers = {
      'Content-Type': 'application/json',
      ...(options.headers || {}),
    };
    if (accessToken) {
      headers['Authorization'] = `Bearer ${accessToken}`;
    }
    return fetch(`${BASE_URL}${path}`, { credentials: 'include', ...options, headers });
  },
};

// ---- Exports -------------------------------------------------------------

if (typeof module !== 'undefined') {
  module.exports = { APIClient, TokenStore, AuthExpiredError, escapeHtml };
} else if (typeof window !== 'undefined') {
  window.APIClient = APIClient;
  window.TokenStore = TokenStore;
  window.AuthExpiredError = AuthExpiredError;
  window.escapeHtml = escapeHtml;
}
