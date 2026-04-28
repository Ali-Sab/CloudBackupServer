/**
 * Unit tests for auth.js pure functions: renderSessionState, escapeHtml, passwordStrength.
 * All tests run in Jest/jsdom — no real backend or Electron required.
 */

'use strict';

global.window = global.window || {};
window._testMode = true;

const { APIClient, AuthExpiredError, escapeHtml } = require('../src/renderer/api-client');
global.APIClient = APIClient;
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

const { renderSessionState, passwordStrength } = require('../src/renderer/auth');

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

describe('passwordStrength', () => {
  test('empty input is score 0', () => {
    expect(passwordStrength('').score).toBe(0);
  });

  test('"password" lands somewhere in the middle', () => {
    const r = passwordStrength('password');
    expect(r.score).toBeGreaterThanOrEqual(1);
    expect(r.score).toBeLessThanOrEqual(2);
  });

  test('long varied passwords score high', () => {
    expect(passwordStrength('Tr0ub4dor&3xtra').score).toBe(4);
  });

  test('short passwords score low', () => {
    expect(passwordStrength('abc').score).toBeLessThanOrEqual(1);
  });
});
