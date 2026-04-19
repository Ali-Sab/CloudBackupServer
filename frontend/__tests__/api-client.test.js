/**
 * Tests for api-client.js — APIClient, TokenStore, AuthExpiredError.
 * All tests run in Jest/jsdom — no real backend or Electron required.
 */

'use strict';

// Expose a minimal window.electronAPI stub so TokenStore uses localStorage.
// (The real IPC bridge is absent in Jest.)
global.window = global.window || {};
window.electronAPI = undefined;

const { APIClient, TokenStore, AuthExpiredError } = require('../src/renderer/api-client');

// ---- helpers ----

function mockFetch(...responses) {
  let call = 0;
  global.fetch = jest.fn(async () => {
    const r = responses[call] ?? responses[responses.length - 1];
    call++;
    return r;
  });
}

function jsonResponse(status, body) {
  return {
    status,
    ok: status >= 200 && status < 300,
    json: async () => body,
  };
}

// ---- TokenStore (browser / localStorage path) ----

describe('TokenStore', () => {
  beforeEach(() => {
    localStorage.clear();
    jest.resetAllMocks();
  });

  test('stores and retrieves access + refresh tokens', () => {
    TokenStore.store('access-123', 'refresh-456');
    expect(TokenStore.getAccessToken()).toBe('access-123');
    expect(TokenStore.getRefreshToken()).toBe('refresh-456');
  });

  test('returns null when no tokens stored', () => {
    expect(TokenStore.getAccessToken()).toBeNull();
    expect(TokenStore.getRefreshToken()).toBeNull();
  });

  test('clear removes both tokens', () => {
    TokenStore.store('a', 'r');
    TokenStore.clear();
    expect(TokenStore.getAccessToken()).toBeNull();
    expect(TokenStore.getRefreshToken()).toBeNull();
  });
});

// ---- TokenStore (Electron / in-memory path) ----

describe('TokenStore in Electron mode', () => {
  let clearRefreshToken, saveRefreshToken, loadRefreshToken;

  beforeEach(() => {
    clearRefreshToken = jest.fn();
    saveRefreshToken  = jest.fn().mockResolvedValue({});
    loadRefreshToken  = jest.fn().mockResolvedValue(null); // no saved token by default
    window.electronAPI = { clearRefreshToken, saveRefreshToken, loadRefreshToken };
    localStorage.clear();
  });

  afterEach(() => {
    // Clear in-memory state while electronAPI is still present so _isElectron() is true
    // and clear() wipes _mem rather than falling through to localStorage.
    TokenStore.clear();
    window.electronAPI = undefined;
  });

  test('store/get use in-memory slots, not localStorage', () => {
    TokenStore.store('el-access', 'el-refresh');
    expect(TokenStore.getAccessToken()).toBe('el-access');
    expect(TokenStore.getRefreshToken()).toBe('el-refresh');
    expect(localStorage.getItem('access_token')).toBeNull();
  });

  test('clear wipes in-memory tokens and calls electronAPI.clearRefreshToken', () => {
    TokenStore.store('el-access', 'el-refresh');
    TokenStore.clear();
    expect(TokenStore.getAccessToken()).toBeNull();
    expect(TokenStore.getRefreshToken()).toBeNull();
    expect(clearRefreshToken).toHaveBeenCalledTimes(1);
  });

  test('store rotates the keychain when a persisted token already exists', async () => {
    loadRefreshToken.mockResolvedValue('old-persisted-token');

    TokenStore.store('new-access', 'new-refresh');

    // Fire-and-forget: wait for the microtask queue to flush.
    await Promise.resolve();
    await Promise.resolve();

    expect(loadRefreshToken).toHaveBeenCalled();
    expect(saveRefreshToken).toHaveBeenCalledWith('new-refresh');
  });

  test('store does not write to keychain when no persisted token exists', async () => {
    // loadRefreshToken already returns null by default in beforeEach.
    TokenStore.store('new-access', 'new-refresh');

    await Promise.resolve();
    await Promise.resolve();

    expect(loadRefreshToken).toHaveBeenCalled();
    expect(saveRefreshToken).not.toHaveBeenCalled();
  });
});

// ---- APIClient.post ----

describe('APIClient.post', () => {
  afterEach(() => jest.resetAllMocks());

  test('sends POST with JSON body, no Authorization header', async () => {
    global.fetch = jest.fn(async () => jsonResponse(200, { ok: true }));

    await APIClient.post('/api/auth/login', { username: 'alice', password: 'pass' });

    const [url, opts] = fetch.mock.calls[0];
    expect(url).toBe(`${APIClient.BASE_URL}/api/auth/login`);
    expect(opts.method).toBe('POST');
    expect(JSON.parse(opts.body)).toEqual({ username: 'alice', password: 'pass' });
    expect(opts.headers['Authorization']).toBeUndefined();
  });
});

// ---- APIClient.request ----

describe('APIClient.request', () => {
  beforeEach(() => {
    localStorage.clear();
    jest.resetAllMocks();
  });

  test('injects Authorization header when access token is stored', async () => {
    TokenStore.store('my-access-token', 'my-refresh-token');
    global.fetch = jest.fn(async () => jsonResponse(200, { logged_in: true }));

    await APIClient.request('/api/session');

    const [, opts] = fetch.mock.calls[0];
    expect(opts.headers['Authorization']).toBe('Bearer my-access-token');
  });

  test('sends no Authorization header when no token stored', async () => {
    global.fetch = jest.fn(async () => jsonResponse(200, { logged_in: false }));

    await APIClient.request('/api/session');

    const [, opts] = fetch.mock.calls[0];
    expect(opts.headers['Authorization']).toBeUndefined();
  });

  test('returns the response directly on success', async () => {
    TokenStore.store('tok', 'ref');
    const expected = jsonResponse(200, { logged_in: true });
    global.fetch = jest.fn(async () => expected);

    const resp = await APIClient.request('/api/session');
    expect(resp.status).toBe(200);
  });

  test('on 401 with refresh token: refreshes then retries with new access token', async () => {
    TokenStore.store('expired-access', 'valid-refresh');

    // First call → 401; refresh call → new tokens; retry → 200
    mockFetch(
      jsonResponse(401, { error: 'unauthorized' }),                        // original request
      jsonResponse(200, { access_token: 'new-access', refresh_token: 'new-refresh', user: {} }), // refresh
      jsonResponse(200, { logged_in: true }),                              // retry
    );

    const resp = await APIClient.request('/api/session');
    expect(resp.status).toBe(200);

    // Retry must use the new access token
    const retryCall = fetch.mock.calls[2];
    expect(retryCall[1].headers['Authorization']).toBe('Bearer new-access');

    // TokenStore must be updated
    expect(TokenStore.getAccessToken()).toBe('new-access');
    expect(TokenStore.getRefreshToken()).toBe('new-refresh');
  });

  test('on 401 with no refresh token: returns the 401 directly (no refresh attempt)', async () => {
    // No tokens stored
    global.fetch = jest.fn(async () => jsonResponse(401, { error: 'unauthorized' }));

    const resp = await APIClient.request('/api/session');
    expect(resp.status).toBe(401);
    expect(fetch).toHaveBeenCalledTimes(1); // no refresh call
  });

  test('on 401 when refresh fails: clears tokens and throws AuthExpiredError', async () => {
    TokenStore.store('expired-access', 'expired-refresh');

    mockFetch(
      jsonResponse(401, { error: 'unauthorized' }), // original
      jsonResponse(401, { error: 'refresh expired' }), // refresh fails
    );

    await expect(APIClient.request('/api/session')).rejects.toThrow(AuthExpiredError);
    expect(TokenStore.getAccessToken()).toBeNull();
    expect(TokenStore.getRefreshToken()).toBeNull();
  });

  test('concurrent 401 responses share a single refresh call', async () => {
    TokenStore.store('expired', 'valid-refresh');

    let refreshCallCount = 0;
    global.fetch = jest.fn(async (url) => {
      if (url.includes('/api/auth/refresh')) {
        refreshCallCount++;
        // Simulate network delay so concurrent calls can pile up
        await new Promise(r => setTimeout(r, 10));
        return jsonResponse(200, { access_token: 'new', refresh_token: 'new-refresh', user: {} });
      }
      // First call per request is 401, second is 200 after refresh
      const calls = fetch.mock.calls.filter(c => c[0].includes('/api/session')).length;
      return jsonResponse(calls <= 2 ? 401 : 200, { logged_in: true });
    });

    // Fire two concurrent requests that will both see 401
    await Promise.all([
      APIClient.request('/api/session'),
      APIClient.request('/api/session'),
    ]);

    expect(refreshCallCount).toBe(1); // only one refresh, not two
  });

  test('on 401 when refresh throws (network error): clears tokens and throws AuthExpiredError', async () => {
    TokenStore.store('access', 'refresh');

    global.fetch = jest.fn(async (url) => {
      if (url.includes('/api/auth/refresh')) throw new Error('Network error');
      return jsonResponse(401, {});
    });

    await expect(APIClient.request('/api/session')).rejects.toThrow(AuthExpiredError);
    expect(TokenStore.getAccessToken()).toBeNull();
  });
});

// ---- APIClient.tryRefresh ----

describe('APIClient.tryRefresh', () => {
  beforeEach(() => {
    localStorage.clear();
    jest.resetAllMocks();
  });

  test('returns false immediately when no refresh token is stored', async () => {
    global.fetch = jest.fn();
    const result = await APIClient.tryRefresh();
    expect(result).toBe(false);
    expect(global.fetch).not.toHaveBeenCalled();
  });

  test('returns true and updates TokenStore when refresh succeeds', async () => {
    TokenStore.store(null, 'saved-refresh-token');
    global.fetch = jest.fn(async () =>
      jsonResponse(200, { access_token: 'new-access', refresh_token: 'new-refresh' })
    );
    const result = await APIClient.tryRefresh();
    expect(result).toBe(true);
    expect(TokenStore.getAccessToken()).toBe('new-access');
    expect(TokenStore.getRefreshToken()).toBe('new-refresh');
  });

  test('returns false and clears tokens when refresh is rejected', async () => {
    TokenStore.store(null, 'expired-refresh-token');
    global.fetch = jest.fn(async () => jsonResponse(401, { error: 'token expired' }));
    const result = await APIClient.tryRefresh();
    expect(result).toBe(false);
    expect(TokenStore.getRefreshToken()).toBeNull();
  });
});

// ---- AuthExpiredError ----

describe('AuthExpiredError', () => {
  test('is an instance of Error', () => {
    const err = new AuthExpiredError();
    expect(err).toBeInstanceOf(Error);
    expect(err.name).toBe('AuthExpiredError');
    expect(err.message).toContain('Session expired');
  });
});
