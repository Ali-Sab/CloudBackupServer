/**
 * Tests for api-client.js — APIClient (cookie-only) and AuthExpiredError.
 * All tests run in Jest/jsdom — no real backend or Electron required.
 */

'use strict';

global.window = global.window || {};

const { APIClient, AuthExpiredError } = require('../src/renderer/api-client');

function jsonResponse(status, body) {
  return {
    status,
    ok: status >= 200 && status < 300,
    json: async () => body,
  };
}

function mockFetch(...responses) {
  let call = 0;
  global.fetch = jest.fn(async () => {
    const r = responses[call] ?? responses[responses.length - 1];
    call++;
    return r;
  });
}

// ---- APIClient.post ----

describe('APIClient.post', () => {
  afterEach(() => jest.resetAllMocks());

  test('sends POST with JSON body and credentials:include', async () => {
    global.fetch = jest.fn(async () => jsonResponse(200, { ok: true }));

    await APIClient.post('/api/auth/login', { email: 'a@b.c', password: 'pass' });

    const [url, opts] = fetch.mock.calls[0];
    expect(url).toBe(`${APIClient.BASE_URL}/api/auth/login`);
    expect(opts.method).toBe('POST');
    expect(opts.credentials).toBe('include');
    expect(JSON.parse(opts.body)).toEqual({ email: 'a@b.c', password: 'pass' });
  });

  test('does not set Authorization header (cookie-only)', async () => {
    global.fetch = jest.fn(async () => jsonResponse(200, { ok: true }));
    await APIClient.post('/api/auth/login', {});
    const [, opts] = fetch.mock.calls[0];
    expect(opts.headers['Authorization']).toBeUndefined();
  });
});

// ---- APIClient.request ----

describe('APIClient.request', () => {
  afterEach(() => jest.resetAllMocks());

  test('returns the response directly on success', async () => {
    global.fetch = jest.fn(async () => jsonResponse(200, { logged_in: true }));
    const resp = await APIClient.request('/api/session');
    expect(resp.status).toBe(200);
  });

  test('does not set Authorization header (cookie-only)', async () => {
    global.fetch = jest.fn(async () => jsonResponse(200, { logged_in: false }));
    await APIClient.request('/api/session');
    const [, opts] = fetch.mock.calls[0];
    expect(opts.credentials).toBe('include');
    expect(opts.headers['Authorization']).toBeUndefined();
  });

  test('on 401: refreshes then retries', async () => {
    mockFetch(
      jsonResponse(401, { error: 'unauthorized' }),  // original
      jsonResponse(200, { ok: true }),               // refresh
      jsonResponse(200, { logged_in: true }),        // retry
    );

    const resp = await APIClient.request('/api/session');
    expect(resp.status).toBe(200);
    expect(fetch).toHaveBeenCalledTimes(3);
    // The second call must be the refresh endpoint.
    expect(fetch.mock.calls[1][0]).toContain('/api/auth/refresh');
  });

  test('on 401 when refresh fails: throws AuthExpiredError', async () => {
    mockFetch(
      jsonResponse(401, {}),
      jsonResponse(401, {}),
    );
    await expect(APIClient.request('/api/session')).rejects.toThrow(AuthExpiredError);
  });

  test('concurrent 401 responses share a single refresh call', async () => {
    let refreshCallCount = 0;
    let sessionCalls = 0;
    global.fetch = jest.fn(async (url) => {
      if (url.includes('/api/auth/refresh')) {
        refreshCallCount++;
        await new Promise(r => setTimeout(r, 10));
        return jsonResponse(200, { ok: true });
      }
      sessionCalls++;
      return jsonResponse(sessionCalls <= 2 ? 401 : 200, { logged_in: true });
    });

    await Promise.all([
      APIClient.request('/api/session'),
      APIClient.request('/api/session'),
    ]);

    expect(refreshCallCount).toBe(1);
  });

  test('on 401 when refresh throws (network error): throws AuthExpiredError', async () => {
    global.fetch = jest.fn(async (url) => {
      if (url.includes('/api/auth/refresh')) throw new Error('Network error');
      return jsonResponse(401, {});
    });
    await expect(APIClient.request('/api/session')).rejects.toThrow(AuthExpiredError);
  });
});

// ---- APIClient.tryRefresh ----

describe('APIClient.tryRefresh', () => {
  afterEach(() => jest.resetAllMocks());

  test('returns true when refresh succeeds', async () => {
    global.fetch = jest.fn(async () => jsonResponse(200, { ok: true }));
    const result = await APIClient.tryRefresh();
    expect(result).toBe(true);
  });

  test('returns false when refresh is rejected', async () => {
    global.fetch = jest.fn(async () => jsonResponse(401, {}));
    const result = await APIClient.tryRefresh();
    expect(result).toBe(false);
  });

  test('returns false on network error', async () => {
    global.fetch = jest.fn(async () => { throw new Error('boom'); });
    const result = await APIClient.tryRefresh();
    expect(result).toBe(false);
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
