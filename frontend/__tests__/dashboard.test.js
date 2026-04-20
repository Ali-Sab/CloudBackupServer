/**
 * Unit tests for dashboard.js pure functions.
 */

'use strict';

global.window = global.window || {};
window._testMode = true;

const { folderHealthStatus, folderFreshnessPercent } = require('../src/renderer/dashboard');

describe('folderHealthStatus', () => {
  test('returns red for null (never backed up)', () => {
    expect(folderHealthStatus(null)).toBe('red');
  });

  test('returns red for undefined', () => {
    expect(folderHealthStatus(undefined)).toBe('red');
  });

  test('returns green for a backup within the last hour', () => {
    const recent = new Date(Date.now() - 30 * 60 * 1000).toISOString();
    expect(folderHealthStatus(recent)).toBe('green');
  });

  test('returns green for a backup exactly 23 hours ago', () => {
    const almostDay = new Date(Date.now() - 23 * 60 * 60 * 1000).toISOString();
    expect(folderHealthStatus(almostDay)).toBe('green');
  });

  test('returns amber for a backup 25 hours ago', () => {
    const old = new Date(Date.now() - 25 * 60 * 60 * 1000).toISOString();
    expect(folderHealthStatus(old)).toBe('amber');
  });

  test('returns amber for a backup 7 days ago', () => {
    const week = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000).toISOString();
    expect(folderHealthStatus(week)).toBe('amber');
  });
});

describe('folderFreshnessPercent', () => {
  test('returns 0 for null', () => {
    expect(folderFreshnessPercent(null)).toBe(0);
  });

  test('returns 0 for undefined', () => {
    expect(folderFreshnessPercent(undefined)).toBe(0);
  });

  test('returns 5 for a backup older than 7 days', () => {
    const old = new Date(Date.now() - 8 * 24 * 60 * 60 * 1000).toISOString();
    expect(folderFreshnessPercent(old)).toBe(5);
  });

  test('returns a high value (>90) for a very recent backup', () => {
    const recent = new Date(Date.now() - 60 * 1000).toISOString();
    expect(folderFreshnessPercent(recent)).toBeGreaterThan(90);
  });

  test('returns a value between 5 and 95 for a mid-range backup', () => {
    const midRange = new Date(Date.now() - 3 * 24 * 60 * 60 * 1000).toISOString();
    const pct = folderFreshnessPercent(midRange);
    expect(pct).toBeGreaterThan(5);
    expect(pct).toBeLessThan(95);
  });
});
