/**
 * API layer — one function per backend endpoint.
 *
 * This is the only file that knows URL strings.
 * All functions return a raw fetch Response so callers can check .ok and call .json().
 * UI modules never call APIClient directly — they go through this layer.
 *
 * Future endpoints (files, sync) go here when backend routes exist.
 */

'use strict';

(function () {

  const _mod = (typeof require !== 'undefined' && typeof module !== 'undefined')
    ? require('./api-client')
    : null;
  const APIClient = _mod ? _mod.APIClient : window.APIClient;

  const API = {

    // ---- Session ----

    fetchSession() {
      return APIClient.request('/api/session');
    },

    // ---- Auth ----

    login(email, password) {
      return APIClient.post('/api/auth/login', { email, password });
    },

    register(email, password) {
      return APIClient.post('/api/auth/register', { email, password });
    },

    logout(refreshToken) {
      return APIClient.post('/api/auth/logout', { refresh_token: refreshToken });
    },

    forgotPassword(email) {
      return APIClient.post('/api/auth/forgot-password', { email });
    },

    resetPassword(resetToken, newPassword) {
      return APIClient.post('/api/auth/reset-password', {
        reset_token: resetToken,
        new_password: newPassword,
      });
    },

    // ---- Files (add when backend routes exist) ----
    // listFiles()           { return APIClient.request('/api/files'); },
    // uploadFile(formData)  { return APIClient.request('/api/files', { method: 'POST', body: formData }); },
    // downloadFile(fileId)  { return APIClient.request(`/api/files/${fileId}`); },
    // deleteFile(fileId)    { return APIClient.request(`/api/files/${fileId}`, { method: 'DELETE' }); },
  };

  if (typeof module !== 'undefined') {
    module.exports = { API };
  } else if (typeof window !== 'undefined') {
    window.API = API;
  }

})();
