/**
 * Unit tests for app.js pure functions: fetchSession, renderSessionState, escapeHtml.
 * All tests run in Jest/jsdom — no real backend or Electron required.
 */

'use strict';

// Prevent the browser-only DOM block from running during import
global.window = global.window || {};
window._testMode = true;

// Provide stub tokenstore so api-client loads cleanly
const { TokenStore } = require('../src/renderer/api-client');

// Expose APIClient on window so app.js can pick it up in non-require mode.
// In Jest (Node), require() path is used, so this isn't strictly needed,
// but keeps the env consistent.
const { APIClient, AuthExpiredError } = require('../src/renderer/api-client');
global.APIClient = APIClient;
global.TokenStore = TokenStore;
global.AuthExpiredError = AuthExpiredError;

const { fetchSession, renderSessionState, escapeHtml } = require('../src/renderer/app');

// ---- fetchSession ----

describe('fetchSession', () => {
  beforeEach(() => {
    localStorage.clear();
    global.fetch = jest.fn();
  });
  afterEach(() => jest.resetAllMocks());

  test('returns session data on success', async () => {
    global.fetch = jest.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ logged_in: false }),
    }));

    const result = await fetchSession();
    expect(result.logged_in).toBe(false);
  });

  test('throws when server returns non-OK status', async () => {
    global.fetch = jest.fn(async () => ({ ok: false, status: 500, json: async () => ({}) }));
    await expect(fetchSession()).rejects.toThrow('HTTP 500');
  });
});

// ---- renderSessionState ----

describe('renderSessionState', () => {
  test('returns logged-out when not authenticated', () => {
    expect(renderSessionState({ logged_in: false }).type).toBe('logged-out');
  });

  test('returns logged-out when logged_in is false even with user object', () => {
    expect(renderSessionState({
      logged_in: false,
      user: { id: 1, username: 'ghost', email: 'g@x.com' },
    }).type).toBe('logged-out');
  });

  test('returns logged-in with user details when authenticated', () => {
    const state = renderSessionState({
      logged_in: true,
      user: { id: 3, username: 'bob', email: 'bob@example.com' },
    });
    expect(state.type).toBe('logged-in');
    expect(state.username).toBe('bob');
    expect(state.email).toBe('bob@example.com');
  });

  test('returns logged-out when logged_in is true but user is missing', () => {
    expect(renderSessionState({ logged_in: true }).type).toBe('logged-out');
  });
});

// ---- escapeHtml ----

describe('escapeHtml', () => {
  test('escapes < and > characters', () => {
    expect(escapeHtml('<script>')).not.toContain('<script>');
    expect(escapeHtml('<script>')).toContain('&lt;');
  });

  test('escapes & character', () => {
    expect(escapeHtml('a & b')).toBe('a &amp; b');
  });

  test('returns plain strings unchanged', () => {
    expect(escapeHtml('hello world')).toBe('hello world');
  });

  test('coerces non-string input to string without throwing', () => {
    expect(() => escapeHtml(42)).not.toThrow();
    expect(escapeHtml(42)).toBe('42');
  });
});
