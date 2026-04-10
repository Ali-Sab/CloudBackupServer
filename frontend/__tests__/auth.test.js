/**
 * Unit tests for auth.js pure functions: renderSessionState, escapeHtml.
 * All tests run in Jest/jsdom — no real backend or Electron required.
 */

'use strict';

// Prevent the browser-only DOM block from running during import
global.window = global.window || {};
window._testMode = true;

// Provide stubs so api-client.js and api.js load cleanly
const { APIClient, TokenStore, AuthExpiredError, escapeHtml } = require('../src/renderer/api-client');
global.APIClient = APIClient;
global.TokenStore = TokenStore;
global.AuthExpiredError = AuthExpiredError;
global.escapeHtml = escapeHtml;

global.API = {
  fetchSession: jest.fn(),
  login: jest.fn(),
  register: jest.fn(),
  logout: jest.fn(),
  forgotPassword: jest.fn(),
  resetPassword: jest.fn(),
};

const { renderSessionState } = require('../src/renderer/auth');

// ---- renderSessionState ----

describe('renderSessionState', () => {
  test('returns logged-out when not authenticated', () => {
    expect(renderSessionState({ logged_in: false }).type).toBe('logged-out');
  });

  test('returns logged-out when logged_in is false even with user object', () => {
    expect(renderSessionState({
      logged_in: false,
      user: { id: 1, email: 'g@x.com' },
    }).type).toBe('logged-out');
  });

  test('returns logged-in with email when authenticated', () => {
    const state = renderSessionState({
      logged_in: true,
      user: { id: 3, email: 'bob@example.com' },
    });
    expect(state.type).toBe('logged-in');
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
