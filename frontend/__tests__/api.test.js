/**
 * Unit tests for api.js — the API layer.
 * All tests run in Jest/jsdom — no real backend or Electron required.
 */

'use strict';

const { APIClient } = require('../src/renderer/api-client');
global.APIClient = APIClient;

const { API } = require('../src/renderer/api');

beforeEach(() => {
  localStorage.clear();
  global.fetch = jest.fn();
});
afterEach(() => jest.resetAllMocks());

// ---- fetchSession ----

describe('API.fetchSession', () => {
  test('returns response on success', async () => {
    global.fetch = jest.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ logged_in: false }),
    }));

    const resp = await API.fetchSession();
    expect(resp.ok).toBe(true);
    const data = await resp.json();
    expect(data.logged_in).toBe(false);
  });

  test('returns non-ok response on server error', async () => {
    global.fetch = jest.fn(async () => ({ ok: false, status: 500, json: async () => ({}) }));
    const resp = await API.fetchSession();
    expect(resp.ok).toBe(false);
    expect(resp.status).toBe(500);
  });
});

// ---- API.login ----

describe('API.login', () => {
  test('posts email and password to /api/auth/login', async () => {
    global.fetch = jest.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ access_token: 'at', refresh_token: 'rt' }),
    }));

    const resp = await API.login('user@example.com', 'secret');
    expect(resp.ok).toBe(true);

    const [url, opts] = global.fetch.mock.calls[0];
    expect(url).toContain('/api/auth/login');
    const body = JSON.parse(opts.body);
    expect(body.email).toBe('user@example.com');
    expect(body.password).toBe('secret');
  });

  test('returns non-ok response on bad credentials', async () => {
    global.fetch = jest.fn(async () => ({
      ok: false,
      status: 401,
      json: async () => ({ error: 'invalid credentials' }),
    }));

    const resp = await API.login('user@example.com', 'wrong');
    expect(resp.ok).toBe(false);
    expect(resp.status).toBe(401);
  });
});

// ---- API.register ----

describe('API.register', () => {
  test('posts email and password to /api/auth/register', async () => {
    global.fetch = jest.fn(async () => ({
      ok: true,
      status: 201,
      json: async () => ({ access_token: 'at', refresh_token: 'rt' }),
    }));

    const resp = await API.register('new@example.com', 'pass123');
    expect(resp.ok).toBe(true);

    const [url, opts] = global.fetch.mock.calls[0];
    expect(url).toContain('/api/auth/register');
    const body = JSON.parse(opts.body);
    expect(body.email).toBe('new@example.com');
    expect(body.password).toBe('pass123');
  });
});
