// Theme handling shared by every page. Loaded from <head>, so the right theme
// is in place before the first paint.
//
// Default is "auto": follow the operating system's light/dark setting and keep
// following it while the page is open. The toggle button cycles
// Auto -> Light -> Dark -> Auto; a chosen Light or Dark is remembered in
// localStorage under 'dnsbench-theme', and Auto removes that key.
(function () {
  var KEY = 'dnsbench-theme';
  var root = document.documentElement;
  var mq = window.matchMedia('(prefers-color-scheme: dark)');
  var ICON = { auto: 'bi bi-circle-half', light: 'bi bi-sun-fill', dark: 'bi bi-moon-stars-fill' };
  var NAME = { auto: 'Auto (follows the system)', light: 'Light', dark: 'Dark' };

  function chosen() {
    try {
      var v = localStorage.getItem(KEY);
      return v === 'light' || v === 'dark' ? v : '';
    } catch (e) { return ''; }
  }
  function mode() { return chosen() || 'auto'; }
  function effective() { return chosen() || (mq.matches ? 'dark' : 'light'); }

  function buttons() {
    var m = mode();
    var next = m === 'auto' ? 'Light' : m === 'light' ? 'Dark' : 'Auto';
    var list = document.querySelectorAll('[data-theme-toggle]');
    for (var i = 0; i < list.length; i++) {
      var icon = list[i].querySelector('i');
      if (icon) icon.className = ICON[m];
      list[i].title = 'Theme: ' + NAME[m] + '. Click for ' + next + '.';
    }
  }

  function apply() {
    var t = effective();
    var changed = root.getAttribute('data-theme') !== t;
    root.setAttribute('data-theme', t);
    root.setAttribute('data-theme-mode', mode());
    buttons();
    if (changed && typeof window.onThemeChange === 'function') window.onThemeChange();
  }

  window.toggleTheme = function () {
    var m = mode();
    var next = m === 'auto' ? 'light' : m === 'light' ? 'dark' : 'auto';
    try {
      if (next === 'auto') localStorage.removeItem(KEY); else localStorage.setItem(KEY, next);
    } catch (e) { /* storage blocked: the choice lasts until reload */ }
    apply();
  };

  if (mq.addEventListener) mq.addEventListener('change', apply); else mq.addListener(apply);
  window.addEventListener('storage', function (e) { if (e.key === KEY) apply(); });
  document.addEventListener('DOMContentLoaded', buttons);
  apply();
})();
