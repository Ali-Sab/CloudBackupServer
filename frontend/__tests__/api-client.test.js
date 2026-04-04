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

// ---- TokenStore ----

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

// ---- AuthExpiredError ----

describe('AuthExpiredError', () => {
  test('is an instance of Error', () => {
    const err = new AuthExpiredError();
    expect(err).toBeInstanceOf(Error);
    expect(err.name).toBe('AuthExpiredError');
    expect(err.message).toContain('Session expired');
  });
});
