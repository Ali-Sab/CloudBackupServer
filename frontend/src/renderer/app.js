/**
 * Boot — entry point for the renderer.
 *
 * Waits for the DOM to be ready, then hands off to Auth.
 * All UI logic lives in auth.js (and future feature modules).
 */

'use strict';

(function () {

  if (typeof document !== 'undefined') {
    document.addEventListener('DOMContentLoaded', function () {
      window.Auth.checkSession();
    });
  }

})();
