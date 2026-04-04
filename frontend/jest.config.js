/** @type {import('jest').Config} */
module.exports = {
  testEnvironment: 'jsdom',
  testMatch: ['**/__tests__/**/*.test.js'],
  // Electron is not available in Jest — mock it so app.js can be required
  moduleNameMapper: {
    electron: '<rootDir>/__mocks__/electron.js',
  },
};
