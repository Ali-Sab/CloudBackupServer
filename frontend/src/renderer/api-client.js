/**
 * API client for Cloud Backup Server.
 *
 * Provides:
 *   - TokenStore  — centralised read/write for access + refresh tokens
 *   - APIClient   — fetch wrapper that auto-refreshes on 401
 *   - AuthExpiredError — thrown when a refresh attempt fails
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
 * TokenStore abstracts over Electron IPC (when running inside Electron) and
 * localStorage (fallback for tests / browser contexts).
 *
 * Access and refresh tokens are always updated together to stay in sync.
 */
const TokenStore = {
  getAccessToken() {
    if (typeof window !== 'undefined' && window.electronAPI) {
      return window.electronAPI.getAccessToken();
    }
    return (typeof localStorage !== 'undefined') ? localStorage.getItem('access_token') : null;
  },

  getRefreshToken() {
    if (typeof window !== 'undefined' && window.electronAPI) {
      return window.electronAPI.getRefreshToken();
    }
    return (typeof localStorage !== 'undefined') ? localStorage.getItem('refresh_token') : null;
  },

  store(accessToken, refreshToken) {
    if (typeof window !== 'undefined' && window.electronAPI) {
      window.electronAPI.setTokens(accessToken, refreshToken);
    } else if (typeof localStorage !== 'undefined') {
      localStorage.setItem('access_token', accessToken);
      localStorage.setItem('refresh_token', refreshToken);
    }
  },

  clear() {
    if (typeof window !== 'undefined' && window.electronAPI) {
      window.electronAPI.clearTokens();
    } else if (typeof localStorage !== 'undefined') {
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
    return fetch(`${BASE_URL}${path}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
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
    return fetch(`${BASE_URL}${path}`, { ...options, headers });
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
