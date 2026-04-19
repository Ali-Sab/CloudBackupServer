/**
 * Unit tests for files.js pure functions.
 * All tests run in Jest — no real Electron, DOM, or backend required.
 */

'use strict';

// Prevent the browser-only IIFE from running (no real electronAPI in Jest).
global.window = global.window || {};
window._testMode = true;

const { buildBackupSummary, formatDate, formatBackupStatusLabel } = require('../src/renderer/files');

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

describe('formatDate', () => {
  test('returns em-dash for falsy input', () => {
    expect(formatDate(0)).toBe('—');
    expect(formatDate(null)).toBe('—');
    expect(formatDate(undefined)).toBe('—');
  });

  test('returns a non-empty string for a valid timestamp', () => {
    const result = formatDate(1_700_000_000_000);
    expect(typeof result).toBe('string');
    expect(result.length).toBeGreaterThan(0);
    expect(result).not.toBe('—');
  });
});

describe('formatBackupStatusLabel', () => {
  test.each([
    ['done',      'Backed up ✓'],
    ['outdated',  'Changed since last backup'],
    ['error',     'Backup failed ✗'],
    ['uploading', 'Uploading…'],
    [undefined,   'Not yet backed up'],
    ['',          'Not yet backed up'],
  ])('status %s → %s', (status, expected) => {
    expect(formatBackupStatusLabel(status)).toBe(expected);
  });
});
