/**
 * Unit tests for files.js pure functions.
 * All tests run in Jest — no real Electron, DOM, or backend required.
 */

'use strict';

// Prevent the browser-only IIFE from running (no real electronAPI in Jest).
global.window = global.window || {};
window._testMode = true;

const { buildBackupSummary, formatDate, formatBackupStatusLabel, fileTypeIcon, filterTree, sortEntries } = require('../src/renderer/files');

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

describe('fileTypeIcon', () => {
  test.each([
    ['photo.jpg',    '🖼️'],
    ['clip.mp4',     '🎬'],
    ['song.mp3',     '🎵'],
    ['report.pdf',   '📕'],
    ['archive.zip',  '📦'],
    ['script.py',    '💻'],
    ['notes.docx',   '📃'],
    ['font.woff2',   '🔤'],
    ['data.sqlite',  '🗄️'],
    ['unknown.xyz',  '📄'],
    ['no-extension', '📄'],
  ])('%s → %s', (filename, icon) => {
    expect(fileTypeIcon(filename)).toBe(icon);
  });

  test('case-insensitive extension matching', () => {
    expect(fileTypeIcon('PHOTO.JPG')).toBe('🖼️');
    expect(fileTypeIcon('Script.JS')).toBe('💻');
  });
});

// ---- Helper to build a minimal children node map ----
function makeChildren(specs) {
  // specs: array of { name, isDirectory, size, modified }
  const children = {};
  for (const s of specs) {
    children[s.name] = {
      entry: {
        name: s.name,
        relativePath: s.name,
        isDirectory: s.isDirectory || false,
        size: s.size || 0,
        modified: s.modified || 0,
      },
      children: {},
    };
  }
  return children;
}

describe('filterTree', () => {
  const children = makeChildren([
    { name: 'alpha.txt' },
    { name: 'beta.jpg' },
    { name: 'gamma.png' },
  ]);

  test('empty query returns all children', () => {
    const result = filterTree(children, '');
    expect(Object.keys(result)).toHaveLength(3);
  });

  test('matches by name substring (case-insensitive)', () => {
    const result = filterTree(children, 'ALPHA');
    expect(Object.keys(result)).toEqual(['alpha.txt']);
  });

  test('returns empty object when nothing matches', () => {
    const result = filterTree(children, 'zzz-no-match');
    expect(Object.keys(result)).toHaveLength(0);
  });

  test('matches multiple files', () => {
    const result = filterTree(children, '.jpg');
    expect(Object.keys(result)).toEqual(['beta.jpg']);
  });

  test('includes a folder if it has a matching descendant', () => {
    const nested = {
      photos: {
        entry: { name: 'photos', relativePath: 'photos', isDirectory: true, size: 0, modified: 0 },
        children: makeChildren([{ name: 'sunset.jpg' }, { name: 'notes.txt' }]),
      },
      docs: {
        entry: { name: 'docs', relativePath: 'docs', isDirectory: true, size: 0, modified: 0 },
        children: makeChildren([{ name: 'readme.md' }]),
      },
    };
    const result = filterTree(nested, 'sunset');
    expect(Object.keys(result)).toContain('photos');
    expect(Object.keys(result)).not.toContain('docs');
    expect(Object.keys(result.photos.children)).toContain('sunset.jpg');
  });

  test('folder whose name matches is included even if no children match', () => {
    const nested = {
      myFolder: {
        entry: { name: 'myFolder', relativePath: 'myFolder', isDirectory: true, size: 0, modified: 0 },
        children: makeChildren([{ name: 'file.dat' }]),
      },
    };
    const result = filterTree(nested, 'myFolder');
    expect(Object.keys(result)).toContain('myFolder');
  });
});

describe('sortEntries', () => {
  const children = makeChildren([
    { name: 'charlie.txt', isDirectory: false, size: 300, modified: 3000 },
    { name: 'alpha.txt',   isDirectory: false, size: 100, modified: 1000 },
    { name: 'beta.txt',    isDirectory: false, size: 200, modified: 2000 },
  ]);

  test('name asc', () => {
    const keys = sortEntries(Object.keys(children), children, 'name', 'asc');
    expect(keys).toEqual(['alpha.txt', 'beta.txt', 'charlie.txt']);
  });

  test('name desc', () => {
    const keys = sortEntries(Object.keys(children), children, 'name', 'desc');
    expect(keys).toEqual(['charlie.txt', 'beta.txt', 'alpha.txt']);
  });

  test('size asc', () => {
    const keys = sortEntries(Object.keys(children), children, 'size', 'asc');
    expect(keys).toEqual(['alpha.txt', 'beta.txt', 'charlie.txt']);
  });

  test('size desc', () => {
    const keys = sortEntries(Object.keys(children), children, 'size', 'desc');
    expect(keys).toEqual(['charlie.txt', 'beta.txt', 'alpha.txt']);
  });

  test('modified asc', () => {
    const keys = sortEntries(Object.keys(children), children, 'modified', 'asc');
    expect(keys).toEqual(['alpha.txt', 'beta.txt', 'charlie.txt']);
  });

  test('modified desc', () => {
    const keys = sortEntries(Object.keys(children), children, 'modified', 'desc');
    expect(keys).toEqual(['charlie.txt', 'beta.txt', 'alpha.txt']);
  });

  test('directories always sort before files regardless of field', () => {
    const mixed = {
      zDir: {
        entry: { name: 'zDir', relativePath: 'zDir', isDirectory: true, size: 9999, modified: 9999 },
        children: {},
      },
      aFile: {
        entry: { name: 'aFile', relativePath: 'aFile', isDirectory: false, size: 1, modified: 1 },
        children: {},
      },
    };
    const nameAsc  = sortEntries(Object.keys(mixed), mixed, 'name', 'asc');
    const sizeDesc = sortEntries(Object.keys(mixed), mixed, 'size', 'desc');
    expect(nameAsc[0]).toBe('zDir');
    expect(sizeDesc[0]).toBe('zDir');
  });

  test('does not mutate the original keys array', () => {
    const keys = Object.keys(children);
    const original = [...keys];
    sortEntries(keys, children, 'name', 'desc');
    expect(keys).toEqual(original);
  });
});
