// Real-browser smoke test for the dnsbench UI (Chromium via Playwright).
//
// Needs: a running dnsbench (plain HTTP, --no-tls), a PAM user, a UDP responder
// on 127.0.0.1:5399 (contrib/perf/reflect.c), README.md and LICENSE.txt next to
// the dnsbench binary, and Bootstrap 5.3.3, Bootstrap Icons 1.11.3 and Chart.js
// 4.4.0 unpacked under CDN_DIR (npm pack them; the page loads them from a CDN
// and this script serves them locally so it works offline).
//
//   PW=$(npm root -g)/playwright CHROME=/path/to/chrome BASE=http://127.0.0.1:8453 \
//   CDN_DIR=/tmp/cdn/ UI_USER=benchuser UI_PASS=secret node ui.js
//
// UI_USER must be a member of the login group (LOGIN_GROUP, default dnsbench). Optionally
// set UI_NONMEMBER_USER and UI_NONMEMBER_PASS to an account that is NOT in the group to
// check it is refused.
//
// Runs the whole flow in light and dark colour schemes, including the theme behaviour.
const { chromium } = require(process.env.PW);
const fs = require('fs');
const CDN = process.env.CDN_DIR || '/tmp/cdn/';
const map = {
  'bootstrap@5.3.3/dist/css/bootstrap.min.css': CDN + 'bootstrap-5.3.3/package/dist/css/bootstrap.min.css',
  'bootstrap@5.3.3/dist/js/bootstrap.bundle.min.js': CDN + 'bootstrap-5.3.3/package/dist/js/bootstrap.bundle.min.js',
  'bootstrap-icons@1.11.3/font/bootstrap-icons.min.css': CDN + 'bootstrap-icons-1.11.3/package/font/bootstrap-icons.min.css',
  'bootstrap-icons@1.11.3/font/fonts/bootstrap-icons.woff2': CDN + 'bootstrap-icons-1.11.3/package/font/fonts/bootstrap-icons.woff2',
  'bootstrap-icons@1.11.3/font/fonts/bootstrap-icons.woff': CDN + 'bootstrap-icons-1.11.3/package/font/fonts/bootstrap-icons.woff',
  'chart.js@4.4.0/dist/chart.umd.min.js': CDN + 'chart.js-4.4.0/package/dist/chart.umd.js',
};
const BASE = process.env.BASE;
const USER = process.env.UI_USER || 'benchuser';
const PASS = process.env.UI_PASS || 'Sup3rSecret!';
let failures = 0;
const check = (name, ok, extra) => { console.log((ok ? 'PASS ' : 'FAIL ') + name + (ok || !extra ? '' : '  -> ' + extra)); if (!ok) failures++; };

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROME, args: ['--no-sandbox'] });
  for (const scheme of ['light', 'dark']) {
    const ctx = await browser.newContext({ colorScheme: scheme, viewport: { width: 1400, height: 1000 } });
    await ctx.route(/cdn\.jsdelivr\.net\/npm\/(.*)/, route => {
      const key = route.request().url().split('/npm/')[1].split('?')[0];
      const f = map[key];
      if (!f) return route.abort();
      const type = f.endsWith('.css') ? 'text/css' : f.endsWith('.js') ? 'application/javascript' : 'font/woff2';
      route.fulfill({ body: fs.readFileSync(f), contentType: type });
    });
    const page = await ctx.newPage();
    const errors = [];
    page.on('pageerror', e => errors.push('pageerror: ' + e.message));
    page.on('console', m => { if (m.type() === 'error') errors.push('console: ' + m.text()); });
    page.on('dialog', d => d.accept());
    page.on('requestfailed', r => console.log('   requestfailed: ' + r.url() + ' ' + (r.failure() && r.failure().errorText)));
    page.on('response', r => { if (r.status() >= 400) console.log('   HTTP ' + r.status() + ': ' + r.request().method() + ' ' + r.url()); });
    console.log(`\n=== ${scheme} ===`);

    // Login page
    await page.goto(BASE + '/login');
    check('login: no 2FA field', (await page.locator('[name=totp]').count()) === 0);
    check('login: subtitle', (await page.textContent('body')).includes('Sign in with your system account'));
    await page.fill('[name=username]', USER);
    await page.fill('[name=password]', 'wrong-password');
    await page.click('button[type=submit]');
    await page.waitForLoadState();
    check('login: wrong password shows error', (await page.textContent('.alert-danger')).includes('Invalid login attempt'));
    if (process.env.UI_NONMEMBER_USER) {
      await page.fill('[name=username]', process.env.UI_NONMEMBER_USER);
      await page.fill('[name=password]', process.env.UI_NONMEMBER_PASS);
      await page.click('button[type=submit]');
      await page.waitForLoadState();
      check('login: right password but not in the login group is refused with the generic error',
        (await page.textContent('.alert-danger')).includes('Invalid login attempt') && page.url().endsWith('/login'));
    }
    await page.fill('[name=username]', USER);
    await page.fill('[name=password]', PASS);
    await Promise.all([page.waitForURL('**/bench'), page.click('button[type=submit]')]);
    await page.waitForLoadState('networkidle');
    if (scheme === 'light') await page.screenshot({ path: (process.env.SHOT_DIR || '/tmp') + '/bench-light.png' });
    else await page.screenshot({ path: (process.env.SHOT_DIR || '/tmp') + '/bench-dark.png' });

    // Benchmark page content
    const protos = await page.$$eval('#protocol option', os => os.map(o => o.value));
    check('protocols are udp,tcp,dot,doh (no doq)', JSON.stringify(protos) === '["udp","tcp","dot","doh"]', JSON.stringify(protos));
    const sprotos = await page.$$eval('#schedProtocol option', os => os.map(o => o.value));
    check('schedule protocols have no doq', JSON.stringify(sprotos) === '["udp","tcp","dot","doh"]', JSON.stringify(sprotos));
    const nav = await page.$$eval('.sidebar .nav-link', as => as.map(a => a.textContent.trim()));
    check('sidebar has no Server Info', JSON.stringify(nav) === '["Benchmark","Schedules","Results","ReadMe","License"]', JSON.stringify(nav));
    check('sidebar shows the signed-in user', (await page.textContent('.sidebar')).includes(USER));
    const ta = await page.inputValue('#queryDomains');
    check('default query domains', ta === 'google.com\ncloudflare.com\ngithub.com', JSON.stringify(ta));
    check('workers placeholder shows auto value', /^Auto: \d+$/.test(await page.getAttribute('#concurrency', 'placeholder')), await page.getAttribute('#concurrency', 'placeholder'));
    check('DoH extras hidden for UDP', !(await page.isVisible('#dohExtras')));
    await page.selectOption('#protocol', 'doh');
    check('DoH extras shown for DoH', await page.isVisible('#dohExtras'));
    check('queue field hidden for DoH', !(await page.isVisible('#pipeline')));
    await page.selectOption('#protocol', 'udp');
    check('queue field shown for UDP', await page.isVisible('#pipeline'));

    // ── Theme ────────────────────────────────────────────────────────────────
    const attr = () => page.getAttribute('html', 'data-bs-theme');
    const mode = () => page.getAttribute('html', 'data-theme-mode');
    const stored = () => page.evaluate(() => localStorage.getItem('dnsbench-theme'));
    check(`theme: starts as ${scheme}, following the system (auto)`, (await attr()) === scheme && (await mode()) === 'auto');
    const lum = async sel => page.evaluate(s => {
      const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
      let e = document.querySelector(s), c;
      for (; e; e = e.parentElement) { c = getComputedStyle(e).backgroundColor.match(/[\d.]+/g).map(Number); if (c.length < 4 || c[3] === 1) break; }
      return 0.2126 * f(c[0]) + 0.7152 * f(c[1]) + 0.0722 * f(c[2]);
    }, sel);
    const wantLight = scheme === 'light';
    for (const sel of ['body', '.sidebar', '.term-wrap', '#server']) {
      const l = await lum(sel);
      check(`theme: ${sel} is ${scheme} (luminance ${l.toFixed(3)}), nothing half-dark or half-light`, wantLight ? l > 0.5 : l < 0.2, String(l));
    }
    const other = wantLight ? 'dark' : 'light';
    const bgBefore = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    await page.emulateMedia({ colorScheme: other });
    await page.waitForFunction(o => document.documentElement.getAttribute('data-bs-theme') === o, other);
    const bgOther = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    check('theme: follows an operating-system change live, without a reload', (await attr()) === other && bgOther !== bgBefore);
    await page.emulateMedia({ colorScheme: scheme });
    await page.waitForFunction(s => document.documentElement.getAttribute('data-bs-theme') === s, scheme);

    const btn = () => page.locator('.sidebar [data-theme-toggle]');
    check('theme toggle explains itself', /Auto/.test(await btn().getAttribute('title')));
    await btn().click();
    check('toggle: auto -> light', (await mode()) === 'light' && (await attr()) === 'light' && (await stored()) === 'light');
    await btn().click();
    check('toggle: light -> dark', (await mode()) === 'dark' && (await attr()) === 'dark' && (await stored()) === 'dark');
    await page.emulateMedia({ colorScheme: 'light' });
    await page.waitForTimeout(150);
    check('toggle: an explicit choice is not overridden by the system', (await attr()) === 'dark');
    await page.reload(); await page.waitForLoadState('networkidle');
    check('toggle: the explicit choice survives a reload', (await attr()) === 'dark' && (await mode()) === 'dark');
    await btn().click();
    await page.emulateMedia({ colorScheme: scheme });
    await page.waitForFunction(s => document.documentElement.getAttribute('data-bs-theme') === s, scheme);
    check('toggle: dark -> auto clears the choice and follows the system again', (await mode()) === 'auto' && (await stored()) === null && (await attr()) === scheme);

    // Text contrast, measured in the rendered page. Controls fade colours over 0.12 s, so
    // switch transitions off first; otherwise a measurement right after a theme change lands mid-fade.
    await page.addStyleTag({ content: '*,*::before,*::after{transition:none !important}' });
    await page.waitForTimeout(200);
    const ratios = await page.evaluate(() => {
      const parse = c => c.match(/[\d.]+/g).map(Number);
      const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
      const lum = c => 0.2126 * f(c[0]) + 0.7152 * f(c[1]) + 0.0722 * f(c[2]);
      const bgOf = el => {
        const layers = [];
        for (let e = el; e; e = e.parentElement) {
          const c = parse(getComputedStyle(e).backgroundColor);
          if (c.length < 4 || c[3] > 0) layers.push(c);
          if (c.length < 4 || c[3] === 1) break;
        }
        let base = [255, 255, 255];
        for (const c of layers.reverse()) { const a = c.length > 3 ? c[3] : 1; base = base.map((b, i) => b * (1 - a) + c[i] * a); }
        return base;
      };
      const ratio = s => {
        const el = document.querySelector(s);
        const fg = parse(getComputedStyle(el).color).slice(0, 3);
        const [a, b] = [lum(fg), lum(bgOf(el))].sort((x, y) => y - x);
        return (a + 0.05) / (b + 0.05);
      };
      return {
        'page title': [ratio('#page-bench h1'), 7],
        'input text': [ratio('#server'), 7],
        'sidebar item': [ratio('.sidebar .nav-link:not(.active)'), 3.5],
        'active sidebar item': [ratio('.sidebar .nav-link.active'), 4.5],
        'field label': [ratio('#page-bench .lbl'), 3.5],
        'terminal text': [ratio('#terminal'), 4.5],
        'query list': [ratio('#queryDomains'), 4.5],
      };
    });
    for (const [what, [r, need]] of Object.entries(ratios)) check(`contrast: ${what} ${r.toFixed(1)}:1 (needs ${need}:1)`, r >= need, r.toFixed(2));

    // Run a benchmark from the UI
    await page.fill('#server', '127.0.0.1:5399');
    await page.fill('#concurrency', '2');
    await page.fill('#pipeline', '64');
    await page.fill('#duration', '3s');
    await page.fill('#queryDomains', 'a.example\nb.example');
    await page.click('#btnRun');
    await page.waitForFunction(() => document.getElementById('jobStatus').textContent.trim() === 'done', null, { timeout: 30000 });
    const term = await page.textContent('#terminal');
    check('terminal shows q/s and success', /\d+ q\/s/.test(term) && term.includes('100% success'), term.slice(-300));
    check('terminal has no raw § markers', !term.includes('§'));
    check('terminal shows resolved line', term.includes('Resolved   : 127.0.0.1 -> 127.0.0.1'));

    // Results page
    await page.evaluate(() => showPage('history', document.querySelector('.nav-link[onclick*="history"]')));
    await page.waitForTimeout(500);
    const hist = await page.textContent('#historyList');
    check('results lists the run', hist.includes('127.0.0.1') && hist.includes('UDP') && /\d+,\d+\s+q\/s/.test(hist) && !hist.includes('No runs yet'), hist.replace(/\s+/g,' ').slice(0, 200));

    // Add a schedule through the real modal
    await page.evaluate(() => showPage('schedules', document.querySelector('.nav-link[onclick*="schedules"]')));
    await page.click('button:has-text("Add Schedule")');
    await page.waitForSelector('#schedModal.show');
    await page.waitForTimeout(400);
    const modalOk = await page.evaluate(() => {
      const probe = document.createElement('div'); probe.style.background = 'var(--bg2)'; document.body.appendChild(probe);
      const want = getComputedStyle(probe).backgroundColor; probe.remove();
      const m = document.querySelector('#schedModal .modal-content');
      const t = document.querySelector('#schedModal .modal-title');
      return getComputedStyle(m).backgroundColor === want && getComputedStyle(t).color !== getComputedStyle(m).backgroundColor;
    });
    check('theme: the schedule modal uses the same palette as the page', modalOk);
    await page.fill('#schedName', 'ui-test');
    await page.fill('#schedServer', '127.0.0.1:5399');
    await page.fill('#schedDuration', '2s');
    await page.fill('#schedQueries', 'a.example');
    await page.selectOption('#schedFreq', 'hourly');
    await page.click('button:has-text("Save Schedule")');
    await page.waitForSelector('#schedModal.show', { state: 'detached' }).catch(() => {});
    await page.waitForFunction(() => document.getElementById('scheduleList').textContent.includes('ui-test'), null, { timeout: 10000 });
    check('schedule appears in list', (await page.textContent('#scheduleList')).includes('ui-test'));
    check('schedule timing label rendered', /Hourly/i.test(await page.textContent('#scheduleList')), (await page.textContent('#scheduleList')).slice(0, 200));

    // Docs pages
    await page.evaluate(() => showPage('readme', document.querySelector('.nav-link[onclick*="readme"]')));
    await page.waitForFunction(() => document.getElementById('readmeContent').textContent.length > 200, null, { timeout: 10000 });
    check('readme page loads', true);
    await page.evaluate(() => showPage('license', document.querySelector('.nav-link[onclick*="license"]')));
    await page.waitForFunction(() => document.getElementById('licenseContent').textContent.includes('GNU GENERAL PUBLIC LICENSE'), null, { timeout: 10000 });
    check('license page loads', true);

    const real = errors.filter(e => !e.includes('401')); // the 401 is the deliberate wrong-password attempt
    check('no JS or resource errors in the browser', real.length === 0, real.join(' | '));
    await ctx.close();
  }
  await browser.close();
  console.log(failures ? `\n${failures} FAILED` : '\nALL UI CHECKS PASSED');
  process.exit(failures ? 1 : 0);
})();
