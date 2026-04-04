/**
 * Unit tests for CloudBackup renderer logic.
 * All tests run in Jest/jsdom — no real backend or Electron required.
 */

// Provide a stub document.createElement so escapeHtml works in jsdom
const CloudBackup = require('../src/renderer/app');

// ---- fetchSession ----

describe('CloudBackup.fetchSession', () => {
  let fetchMock;

  beforeEach(() => {
    fetchMock = jest.fn();
    global.fetch = fetchMock;
  });

  afterEach(() => {
    jest.resetAllMocks();
  });

  test('calls /api/session with no Authorization header when token is null', async () => {
    fetchMock.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ logged_in: false }),
    });

    const result = await CloudBackup.fetchSession(null);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, opts] = fetchMock.mock.calls[0];
    expect(url).toBe(`${CloudBackup.API_BASE_URL}/api/session`);
    expect(opts.headers).not.toHaveProperty('Authorization');
    expect(result.logged_in).toBe(false);
  });

  test('calls /api/session with Authorization header when token is provided', async () => {
    fetchMock.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        logged_in: true,
        user: { id: 7, username: 'alice', email: 'alice@example.com' },
      }),
    });

    const result = await CloudBackup.fetchSession('my.jwt.token');

    const [, opts] = fetchMock.mock.calls[0];
    expect(opts.headers['Authorization']).toBe('Bearer my.jwt.token');
    expect(result.logged_in).toBe(true);
    expect(result.user.username).toBe('alice');
  });

  test('throws when the server returns a non-OK status', async () => {
    fetchMock.mockResolvedValueOnce({ ok: false, status: 503 });

    await expect(CloudBackup.fetchSession(null)).rejects.toThrow('HTTP 503');
  });

  test('throws when fetch itself rejects (network error)', async () => {
    fetchMock.mockRejectedValueOnce(new Error('Network failure'));

    await expect(CloudBackup.fetchSession(null)).rejects.toThrow('Network failure');
  });
});

// ---- renderSessionState ----

describe('CloudBackup.renderSessionState', () => {
  test('returns logged-out descriptor when logged_in is false', () => {
    const state = CloudBackup.renderSessionState({ logged_in: false });
    expect(state.type).toBe('logged-out');
    expect(state.username).toBeUndefined();
  });

  test('returns logged-out descriptor when logged_in is false even with a user object', () => {
    const state = CloudBackup.renderSessionState({
      logged_in: false,
      user: { id: 1, username: 'ghost', email: 'ghost@example.com' },
    });
    expect(state.type).toBe('logged-out');
  });

  test('returns logged-in descriptor with user details when authenticated', () => {
    const state = CloudBackup.renderSessionState({
      logged_in: true,
      user: { id: 3, username: 'bob', email: 'bob@example.com' },
    });
    expect(state.type).toBe('logged-in');
    expect(state.username).toBe('bob');
    expect(state.email).toBe('bob@example.com');
  });

  test('returns logged-out when logged_in is true but user is missing', () => {
    const state = CloudBackup.renderSessionState({ logged_in: true });
    expect(state.type).toBe('logged-out');
  });
});

// ---- escapeHtml ----

describe('CloudBackup.escapeHtml', () => {
  test('escapes < and > characters', () => {
    expect(CloudBackup.escapeHtml('<script>')).not.toContain('<script>');
  });

  test('escapes & character', () => {
    expect(CloudBackup.escapeHtml('a & b')).toBe('a &amp; b');
  });

  test('returns plain strings unchanged', () => {
    expect(CloudBackup.escapeHtml('hello world')).toBe('hello world');
  });

  test('coerces non-string input to string', () => {
    expect(() => CloudBackup.escapeHtml(42)).not.toThrow();
  });
});
