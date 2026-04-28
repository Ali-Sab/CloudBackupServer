/**
 * API client for Cloud Backup Server.
 *
 * Provides:
 *   - APIClient        — fetch wrapper with cookie auth and a single-flight refresh
 *   - AuthExpiredError — thrown when a refresh attempt fails
 *   - escapeHtml       — XSS-safe HTML escaping (shared utility)
 *
 * Auth is cookie-only. The browser/Electron session attaches the access_token
 * cookie automatically. There are no tokens in memory or localStorage.
 */

'use strict';

// BASE_URL is mutable — the Settings panel updates it without restart.
let BASE_URL = (typeof process !== 'undefined' && process.env.API_BASE_URL)
  ? process.env.API_BASE_URL
  : (typeof localStorage !== 'undefined' && localStorage.getItem('settings_server_url'))
  || 'http://localhost:8080';

/** XSS-safe HTML escaping. Canonical definition — shared by other modules. */
function escapeHtml(str) {
  const div = document.createElement('div');
  div.appendChild(document.createTextNode(String(str)));
  return div.innerHTML;
}

class AuthExpiredError extends Error {
  constructor() {
    super('Session expired — please log in again');
    this.name = 'AuthExpiredError';
  }
}

// ---- Refresh lock --------------------------------------------------------
// Ensures concurrent 401 responses share a single refresh call.

let _refreshPromise = null;

async function _doRefresh() {
  try {
    const resp = await fetch(`${BASE_URL}/api/auth/refresh`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
    });
    return resp.ok;
  } catch {
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
  get BASE_URL() { return BASE_URL; },
  set BASE_URL(v) { BASE_URL = v; },

  /**
   * Make an authenticated request. Cookies attach automatically.
   * On 401, attempts one refresh + retry; if refresh fails, throws AuthExpiredError.
   */
  async request(path, options = {}) {
    let resp = await this._doRequest(path, options);
    if (resp.status === 401) {
      const refreshed = await _refreshOnce();
      if (!refreshed) throw new AuthExpiredError();
      resp = await this._doRequest(path, options);
    }
    return resp;
  },

  /** Unauthenticated POST (login, register, forgot-password, etc.). */
  async post(path, body) {
    return fetch(`${BASE_URL}${path}`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
  },

  /** Authenticated PUT with a JSON body. */
  put(path, body) {
    return this.request(path, { method: 'PUT', body: JSON.stringify(body) });
  },

  /**
   * Try a single proactive refresh. Used at startup to revive a persisted
   * session — GET /api/session always returns 200 so the auto-refresh in
   * request() never fires on its own there.
   */
  tryRefresh() {
    return _refreshOnce();
  },

  async _doRequest(path, options = {}) {
    const headers = {
      'Content-Type': 'application/json',
      ...(options.headers || {}),
    };
    return fetch(`${BASE_URL}${path}`, { credentials: 'include', ...options, headers });
  },
};

// ---- Exports -------------------------------------------------------------

if (typeof module !== 'undefined') {
  module.exports = { APIClient, AuthExpiredError, escapeHtml };
} else if (typeof window !== 'undefined') {
  window.APIClient = APIClient;
  window.AuthExpiredError = AuthExpiredError;
  window.escapeHtml = escapeHtml;
}
