/**
 * Unit tests for files.js pure functions.
 * All tests run in Jest — no real Electron, DOM, or backend required.
 */

'use strict';

// Prevent the browser-only IIFE from running (no real electronAPI in Jest).
global.window = global.window || {};
window._testMode = true;

const { buildBackupSummary } = require('../src/renderer/files');

describe('buildBackupSummary', () => {
  test('returns null for empty results', () => {
    expect(buildBackupSummary([])).toBeNull();
  });

  test('counts uploaded files', () => {
    const result = buildBackupSummary([
      { error: null, skipped: false },
      { error: null, skipped: false },
    ]);
    expect(result).not.toBeNull();
    expect(result.message).toContain('2 uploaded');
    expect(result.type).toBe('success');
  });

  test('counts skipped (unchanged) files', () => {
    const result = buildBackupSummary([
      { error: null, skipped: true },
      { error: null, skipped: true },
      { error: null, skipped: true },
    ]);
    expect(result.message).toContain('3 unchanged');
    expect(result.type).toBe('success');
  });

  test('counts failed files', () => {
    const result = buildBackupSummary([
      { error: 'network error', skipped: false },
    ]);
    expect(result.message).toContain('1 failed');
    expect(result.type).toBe('error');
  });

  test('combines all three categories', () => {
    const result = buildBackupSummary([
      { error: null, skipped: false },
      { error: null, skipped: true },
      { error: 'oops', skipped: false },
    ]);
    expect(result.message).toContain('1 uploaded');
    expect(result.message).toContain('1 unchanged');
    expect(result.message).toContain('1 failed');
    expect(result.type).toBe('error');
  });

  test('type is success when there are uploads and skips but no failures', () => {
    const result = buildBackupSummary([
      { error: null, skipped: false },
      { error: null, skipped: true },
    ]);
    expect(result.type).toBe('success');
  });

  test('omits zero-count categories from the message', () => {
    const result = buildBackupSummary([
      { error: null, skipped: false },
    ]);
    expect(result.message).not.toContain('unchanged');
    expect(result.message).not.toContain('failed');
  });
});
