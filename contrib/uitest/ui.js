// Real-browser smoke test for the dnsbench UI (Chromium via Playwright).
//
// Needs: a running dnsbench (plain HTTP, --no-tls), a PAM user, a UDP responder
// on 127.0.0.1:5399 (contrib/perf/reflect.c), and README.md and LICENSE.txt next to
// the dnsbench binary. Nothing else: the UI ships with the daemon and loads nothing
// from other hosts. The test enforces that by refusing every request that does not go
// to BASE and failing if the page attempts one.
//
//   PW=$(npm root -g)/playwright CHROME=/path/to/chrome BASE=http://127.0.0.1:8453 \
//   UI_USER=benchuser UI_PASS=secret node ui.js
//
// UI_USER must be a member of the login group (LOGIN_GROUP, default dnsbench). Optionally
// set UI_NONMEMBER_USER and UI_NONMEMBER_PASS to an account that is NOT in the group to
// check it is refused.
//
// Runs the whole flow in light and dark colour schemes, including the theme behaviour.
const { chromium } = require(process.env.PW);
const fs = require('fs');
const os = require('os');
const path = require('path');
const { execFileSync } = require('child_process');
const BASE = process.env.BASE;
const USER = process.env.UI_USER || 'benchuser';
const PASS = process.env.UI_PASS || 'Sup3rSecret!';
let failures = 0;
const external = new Set(); // requests the page tried to send anywhere but BASE
const check = (name, ok, extra) => { console.log((ok ? 'PASS ' : 'FAIL ') + name + (ok || !extra ? '' : '  -> ' + extra)); if (!ok) failures++; };

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROME, args: ['--no-sandbox'] });
  for (const scheme of ['light', 'dark']) {
    const ctx = await browser.newContext({ colorScheme: scheme, viewport: { width: 1400, height: 1000 } });
    await ctx.route(u => !u.href.startsWith(BASE), route => { external.add(route.request().url()); route.abort(); });
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
    check('login: no ReadMe or License links', (await page.locator('a[href="/readme"], a[href="/license"]').count()) === 0);
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
    check('sidebar has no Server Info', JSON.stringify(nav) === '["Benchmark","Results","Schedules","Updates","ReadMe","License"]', JSON.stringify(nav));
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
    const attr = () => page.getAttribute('html', 'data-theme');
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
    await page.waitForFunction(o => document.documentElement.getAttribute('data-theme') === o, other);
    const bgOther = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    check('theme: follows an operating-system change live, without a reload', (await attr()) === other && bgOther !== bgBefore);
    await page.emulateMedia({ colorScheme: scheme });
    await page.waitForFunction(s => document.documentElement.getAttribute('data-theme') === s, scheme);

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
    await page.waitForFunction(s => document.documentElement.getAttribute('data-theme') === s, scheme);
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

    // Benchmark page: Setup and Output tabs, output card on its own tab
    check('bench: Setup and Output tabs exist', (await page.locator('#tab-setup').count()) === 1 && (await page.locator('#tab-output').count()) === 1);
    check('bench: Setup tab is shown first, output is hidden', await page.isVisible('#btnRun') && !(await page.isVisible('#terminal')));
    check('bench: the terminal lives in the Output pane', (await page.locator('#bench-pane-output #terminal').count()) === 1);
    await page.click('#tab-output');
    check('bench: clicking Output shows the terminal and hides the form', await page.isVisible('#terminal') && !(await page.isVisible('#btnRun')));
    await page.click('#tab-setup');
    check('bench: clicking Setup brings the form back', await page.isVisible('#btnRun') && !(await page.isVisible('#terminal')));

    // Run a benchmark from the UI
    await page.fill('#server', '127.0.0.1:5399');
    await page.fill('#concurrency', '2');
    await page.fill('#pipeline', '64');
    await page.fill('#duration', '3s');
    await page.fill('#queryDomains', 'a.example\nb.example');
    await page.click('#btnRun');
    check('bench: starting a run opens the Output tab', await page.isVisible('#terminal') && await page.locator('#tab-output.active').count() === 1);
    // ...and when it completes, the Results page opens with the new run highlighted
    await page.waitForFunction(() => document.getElementById('page-history').classList.contains('active'), null, { timeout: 30000 });
    check('bench: completion opens the Results page', await page.isVisible('#historyList') && !(await page.isVisible('#btnRun')));
    check('bench: the Results nav link is the active one', await page.locator('.nav-link.active', { hasText: 'Results' }).count() === 1);
    await page.waitForSelector('#historyList .run-card.hl', { timeout: 10000 });
    check('results: exactly the new run is highlighted', (await page.locator('#historyList .run-card.hl').count()) === 1 && (await page.textContent('#historyList .run-card.hl')).includes('New'));
    const hlOk = await page.evaluate(() => {
      const c = document.querySelector('#historyList .run-card.hl'), r = c.getBoundingClientRect(), cs = getComputedStyle(c);
      return { inView: r.top >= 0 && r.bottom <= innerHeight, border: cs.borderTopWidth, color: cs.borderTopColor };
    });
    check('results: the highlighted run is on screen with an accent outline', hlOk.inView && hlOk.border === '2px', JSON.stringify(hlOk));
    const status = await page.textContent('#jobStatus');
    check('bench: status reads done', status.trim() === 'done', status);
    const term = await page.textContent('#terminal');
    check('terminal shows q/s and success', /\d+ q\/s/.test(term) && term.includes('100% success'), term.slice(-300));
    check('terminal has no raw § markers', !term.includes('§'));
    check('terminal shows resolved line', term.includes('Resolved   : 127.0.0.1 -> 127.0.0.1'));
    const hist = await page.textContent('#historyList');
    check('results lists the run', hist.includes('127.0.0.1') && hist.includes('UDP') && /\d+,\d+\s+q\/s/.test(hist) && !hist.includes('No runs yet'), hist.replace(/\s+/g,' ').slice(0, 200));
    // leaving the Results page drops the highlight
    await page.evaluate(() => showPage('bench', document.querySelector('.nav-link[onclick*="bench"]')));
    await page.evaluate(() => showPage('history', document.querySelector('.nav-link[onclick*="history"]')));
    await page.waitForTimeout(500);
    check('results: the highlight is gone after leaving and coming back', (await page.locator('#historyList .run-card.hl').count()) === 0);

    // Add a schedule through the real modal
    await page.evaluate(() => showPage('schedules', document.querySelector('.nav-link[onclick*="schedules"]')));
    await page.click('button:has-text("Add Schedule")');
    await page.waitForSelector('#schedModal[open]');
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
    await page.waitForSelector('#schedModal[open]', { state: 'detached' });
    await page.waitForFunction(() => document.getElementById('scheduleList').textContent.includes('ui-test'), null, { timeout: 10000 });
    check('schedule appears in list', (await page.textContent('#scheduleList')).includes('ui-test'));
    check('schedule timing label rendered', /Hourly/i.test(await page.textContent('#scheduleList')), (await page.textContent('#scheduleList')).slice(0, 200));

    // Escape closes the dialog; the backdrop closes it too
    await page.click('button:has-text("Add Schedule")');
    await page.waitForSelector('#schedModal[open]');
    await page.keyboard.press('Escape');
    await page.waitForSelector('#schedModal[open]', { state: 'detached' });
    check('dialog: Escape closes it', true);
    await page.click('button:has-text("Add Schedule")');
    await page.waitForSelector('#schedModal[open]');
    await page.mouse.click(5, 5); // on the dimmed backdrop
    await page.waitForSelector('#schedModal[open]', { state: 'detached' });
    check('dialog: a click on the backdrop closes it', true);

    // Schedules: no button bar; actions live in a right-click menu
    await page.evaluate(() => showPage('schedules', document.querySelector('.nav-link[onclick*="schedules"]')));
    await page.waitForSelector('.sched-row');
    check('schedules: the bulk button bar is gone', (await page.locator('#schedBulkBar').count()) === 0);
    const pageButtons = await page.locator('#page-schedules button:visible').allTextContents();
    check('schedules: the only button on the page is Add Schedule', pageButtons.length === 1 && /Add Schedule/.test(pageButtons[0]), JSON.stringify(pageButtons));
    const row0 = page.locator('.sched-row', { hasText: 'ui-test' }).first();
    await row0.click({ button: 'right' });
    const labels = () => page.locator('#schedMenu .ctx-item').allTextContents().then(a => a.map(t => t.trim()));
    check('schedules: right-click opens the menu with Edit, Duplicate, Run now, Pause, Delete in that order',
      await page.isVisible('#schedMenu') && JSON.stringify(await labels()) === JSON.stringify(['Edit', 'Duplicate', 'Run now', 'Pause', 'Delete']), JSON.stringify(await labels()));
    const menuBox = await page.locator('#schedMenu').boundingBox();
    check('schedules: the menu is fully on screen', menuBox.x >= 0 && menuBox.y >= 0 && menuBox.x + menuBox.width <= 1400 && menuBox.y + menuBox.height <= 1000, JSON.stringify(menuBox));
    const menuTheme = await page.evaluate(() => {
      const probe = document.createElement('div'); probe.style.background = 'var(--bg2)'; document.body.appendChild(probe);
      const want = getComputedStyle(probe).backgroundColor; probe.remove();
      const m = getComputedStyle(document.getElementById('schedMenu')), it = getComputedStyle(document.querySelector('#schedMenu .ctx-item'));
      return { same: m.backgroundColor === want, readable: it.color !== m.backgroundColor };
    });
    check('theme: the menu uses the page palette and its text is readable', menuTheme.same && menuTheme.readable, JSON.stringify(menuTheme));
    await page.keyboard.press('Escape');
    check('schedules: Escape closes the menu', !(await page.isVisible('#schedMenu')));
    await row0.click({ button: 'right' });
    await page.mouse.click(5, 500);
    check('schedules: a click elsewhere closes the menu', !(await page.isVisible('#schedMenu')));

    // Edit
    await row0.click({ button: 'right' });
    await page.click('#schedMenu .ctx-item:has-text("Edit")');
    await page.waitForSelector('#schedModal[open]');
    check('schedules: Edit opens the editor on this schedule', (await page.textContent('#schedModalTitle')) === 'Edit Schedule' && (await page.inputValue('#schedName')) === 'ui-test' && (await page.inputValue('#schedId')) !== '');
    await page.keyboard.press('Escape');
    await page.waitForSelector('#schedModal[open]', { state: 'detached' });

    // Duplicate: prefilled copy, saved as a NEW schedule (counts are relative: the light run's schedules are still there in the dark run)
    const rowsBefore = await page.locator('.sched-row').count();
    await row0.click({ button: 'right' });
    await page.click('#schedMenu .ctx-item:has-text("Duplicate")');
    await page.waitForSelector('#schedModal[open]');
    check('schedules: Duplicate opens a pre-filled form with no id and "(copy)" in the name',
      (await page.textContent('#schedModalTitle')) === 'Duplicate Schedule' && (await page.inputValue('#schedName')) === 'ui-test (copy)'
      && (await page.inputValue('#schedId')) === '' && (await page.inputValue('#schedServer')) === '127.0.0.1:5399' && (await page.inputValue('#schedDuration')) === '2s');
    await page.click('button:has-text("Save Schedule")');
    await page.waitForSelector('#schedModal[open]', { state: 'detached' });
    await page.waitForFunction(n => document.querySelectorAll('.sched-row').length === n + 1, rowsBefore, { timeout: 10000 });
    check('schedules: the duplicate is added alongside the original, which is kept',
      (await page.locator('.sched-row', { hasText: 'ui-test (copy)' }).count()) === 1 && (await page.locator('.sched-row').count()) === rowsBefore + 1);

    // Run now (the request is stubbed so no benchmark is started behind the rest of the test)
    let runBody = null;
    await page.route('**/api/schedules/run', route => { runBody = route.request().postData(); route.fulfill({ contentType: 'application/json', body: JSON.stringify({ ok: true, job_id: 'abcdef1234567890' }) }); });
    await row0.click({ button: 'right' });
    await page.click('#schedMenu .ctx-item:has-text("Run now")');
    await page.waitForFunction(() => document.body.textContent.includes('Started job abcdef12'), null, { timeout: 5000 });
    await page.unroute('**/api/schedules/run');
    check('schedules: Run now posts this schedule id and reports the job', runBody && JSON.parse(runBody).id.length > 0);

    // Pause then Resume, from the menu
    const copyRow = page.locator('.sched-row', { hasText: 'ui-test (copy)' });
    await copyRow.click({ button: 'right' });
    await page.click('#schedMenu .ctx-item:has-text("Pause")');
    await page.waitForFunction(() => [...document.querySelectorAll('.sched-row')].some(r => r.textContent.includes('(copy)') && r.textContent.includes('Paused')), null, { timeout: 10000 });
    check('schedules: Pause marks the schedule as paused', true);
    const pausedLayout = await page.evaluate(() => { const r = [...document.querySelectorAll('.sched-row')].find(x => x.textContent.includes('(copy)')); const cs = getComputedStyle(r); return { display: cs.display, opacity: cs.opacity }; });
    check('schedules: a paused row keeps its layout and is dimmed (a missing semicolon used to drop display:grid)', pausedLayout.display === 'grid' && parseFloat(pausedLayout.opacity) < 1, JSON.stringify(pausedLayout));
    await copyRow.click({ button: 'right' });
    check('schedules: the menu now offers Resume', (await labels()).includes('Resume') && !(await labels()).includes('Pause'), JSON.stringify(await labels()));
    await page.click('#schedMenu .ctx-item:has-text("Resume")');
    await page.waitForFunction(() => ![...document.querySelectorAll('.sched-row')].some(r => r.textContent.includes('(copy)') && r.textContent.includes('Paused')), null, { timeout: 10000 });
    check('schedules: Resume clears it', true);

    // Several ticked rows: the menu acts on all of them; single-schedule items are disabled
    await page.locator('.sched-row .sched-cb').nth(0).check();
    await page.locator('.sched-row .sched-cb').nth(1).check();
    await page.locator('.sched-row').nth(0).click({ button: 'right' });
    const multi = await labels();
    const disabled = await page.locator('#schedMenu .ctx-item[aria-disabled="true"]').allTextContents().then(a => a.map(t => t.trim()));
    check('schedules: with two ticked the menu says so and Edit, Duplicate and Run now are disabled',
      multi.includes('Delete 2 schedules') && multi.includes('Pause 2 schedules') && JSON.stringify(disabled) === JSON.stringify(['Edit', 'Duplicate', 'Run now']), JSON.stringify([multi, disabled]));
    await page.keyboard.press('Escape');
    await page.locator('.sched-row .sched-cb').nth(0).uncheck();
    await page.locator('.sched-row .sched-cb').nth(1).uncheck();

    // Keyboard: the contextmenu event with no pointer position (Menu key / Shift+F10) opens it at the row, and arrows move
    await page.evaluate(() => { const r = document.querySelector('.sched-row'); r.focus(); r.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 0, clientY: 0 })); });
    const kbd = await page.evaluate(() => { const m = document.getElementById('schedMenu').getBoundingClientRect(), r = document.querySelector('.sched-row').getBoundingClientRect(); return { open: !document.getElementById('schedMenu').hidden, nearRow: Math.abs(m.top - (r.top + r.height / 2)) < 60, focused: document.activeElement.textContent.trim() }; });
    check('schedules: a keyboard-triggered menu opens at the row with the first item focused', kbd.open && kbd.nearRow && kbd.focused === 'Edit', JSON.stringify(kbd));
    await page.keyboard.press('ArrowDown');
    check('schedules: ArrowDown moves to the next item', (await page.evaluate(() => document.activeElement.textContent.trim())) === 'Duplicate');
    await page.keyboard.press('Escape');
    check('schedules: Escape from the keyboard returns focus to the row', await page.evaluate(() => document.activeElement.classList.contains('sched-row')));

    // Delete (the browser confirm is auto-accepted by the dialog handler above)
    await copyRow.click({ button: 'right' });
    await page.click('#schedMenu .ctx-item:has-text("Delete")');
    await page.waitForFunction(() => ![...document.querySelectorAll('.sched-row')].some(r => r.textContent.includes('(copy)')), null, { timeout: 10000 });
    check('schedules: Delete removes just that schedule', (await page.locator('.sched-row').count()) === rowsBefore && (await page.locator('.sched-row', { hasText: '(copy)' }).count()) === 0);
    // leaving the page closes an open menu
    await page.locator('.sched-row').first().click({ button: 'right' });
    await page.evaluate(() => showPage('bench', document.querySelector('.nav-link[onclick*="bench"]')));
    check('schedules: navigating away closes the menu', !(await page.isVisible('#schedMenu')));
    await page.evaluate(() => showPage('schedules', document.querySelector('.nav-link[onclick*="schedules"]')));

    // Comparison: three runs, one with errors, as side-by-side pie charts
    await page.evaluate(() => {
      const mk = (started, server, qps, p95, errors, sent) => ({ started, args: { server, protocol: 'udp' }, stats: { qps, p95, errors, sent } });
      runHistory.length = 0;
      runHistory.push(mk('2026-10-05 09:00:01', '1.1.1.1', 48210, 12.4, 0, 96000));
      runHistory.push(mk('2026-10-05 09:05:44', '8.8.8.8', 35120, 38.9, 420, 70000));
      runHistory.push(mk('2026-10-05 09:10:12', '192.168.1.1', 1200, 71.3, 64, 2400));
      runHistory.forEach(r => { r._selected = true; });
      showPage('history', document.querySelector('.nav-link[onclick*="history"]'));
      compareSelected();
    });
    await page.waitForTimeout(500);
    const painted = id => page.evaluate(i => {
      const c = document.getElementById(i), d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
      const bg = [d[0], d[1], d[2]]; let diff = 0;
      for (let k = 0; k < d.length; k += 4 * 7) if (Math.abs(d[k] - bg[0]) + Math.abs(d[k + 1] - bg[1]) + Math.abs(d[k + 2] - bg[2]) > 40) diff++;
      return { w: c.width, h: c.height, diff };
    }, id);
    const q = await painted('compareQpsCanvas'), p95 = await painted('compareP95Canvas');
    check('charts: throughput pie is drawn', q.w > 100 && q.h > 50 && q.diff > 200, JSON.stringify(q));
    check('charts: p95 pie is drawn', p95.w > 100 && p95.h > 50 && p95.diff > 200, JSON.stringify(p95));
    const box = await page.locator('#compareQpsCanvas').boundingBox();
    const box2 = await page.locator('#compareP95Canvas').boundingBox();
    check('charts: the two pies sit side by side', box2.x >= box.x + box.width - 1 && Math.abs(box2.y - box.y) < 4, JSON.stringify([box, box2]));
    check('charts: legend lists every run with its errors', (await page.locator('#compareLegend .pie-legend-row').count()) === 3 && (await page.textContent('#compareLegend')).includes('420 errors'), await page.textContent('#compareLegend'));
    // Hover: sweep the throughput pie until a slice answers
    let tip = '';
    const cx = box.x + box.width / 2, cy = box.y + box.height / 2, r = Math.min(box.width, box.height) / 2 - 20;
    for (let ang = 0; ang < 360 && !tip; ang += 6) {
      await page.mouse.move(cx + Math.cos(ang * Math.PI / 180) * r * 0.6, cy + Math.sin(ang * Math.PI / 180) * r * 0.6);
      const t = await page.$('[role=tooltip]');
      if (t && await t.isVisible()) tip = await t.innerText();
    }
    check('charts: hovering a slice shows run, rate, share and errors', /\d+\.\d\.\d\.\d|192/.test(tip) && /q\/s/.test(tip) && /%/.test(tip) && /errors/.test(tip), tip);
    const caps = await page.locator('.pie-card').evaluateAll(cs => cs.map(c => c.querySelector('.pie-title').textContent + '|' + c.querySelector('.pie-caption').textContent));
    check('charts: throughput pie says "Larger is better", p95 pie says "Smaller is better"',
      caps.length === 2 && /Throughput.*\|Larger is better$/.test(caps[0]) && /p95.*\|Smaller is better$/.test(caps[1]), JSON.stringify(caps));

    // Timestamps: server ISO (UTC) and browser stamps come out in one format, and sort together
    const ts = await page.evaluate(() => ({
      iso:      normStarted('2026-10-05T10:03:55Z'),
      frac:     normStarted('2026-10-05T10:03:55.250Z'),
      offset:   normStarted('2026-10-05T12:03:55+02:00'),
      noZone:   normStarted('2026-10-05T10:03:55'),
      already:  normStarted('2026-10-05 14:34:51'),
      junk:     normStarted('not a time'),
      empty:    normStarted(undefined),
    }));
    check('timestamps: server ISO becomes "YYYY-MM-DD HH:MM:SS" (UTC)', ts.iso === '2026-10-05 10:03:55' && ts.frac === '2026-10-05 10:03:55' && ts.offset === '2026-10-05 10:03:55', JSON.stringify(ts));
    check('timestamps: a zone-less ISO time is read as UTC, not browser-local', ts.noZone === '2026-10-05 10:03:55', JSON.stringify(ts));
    check('timestamps: the browser format and unparseable text are left alone', ts.already === '2026-10-05 14:34:51' && ts.junk === 'not a time' && ts.empty === '', JSON.stringify(ts));
    // History already saved with ISO stamps is repaired on load, and then sorts by real time
    const rep = await page.evaluate(() => {
      localStorage.setItem(HISTORY_KEY, JSON.stringify([
        { started: '2026-10-05T10:03:55Z', args: { server: 'a' }, stats: { qps: 1 } },
        { started: '2026-10-05 14:34:51',  args: { server: 'b' }, stats: { qps: 2 } },
        { started: '2026-10-05T12:00:00Z', args: { server: 'c' }, stats: { qps: 3 } },
      ]));
      const loaded = loadHistory();
      localStorage.removeItem(HISTORY_KEY);
      loaded.sort((x, y) => (y.started < x.started ? -1 : 1));
      return loaded.map(r => r.args.server + '@' + r.started);
    });
    check('timestamps: saved history is repaired on load and orders newest first',
      JSON.stringify(rep) === JSON.stringify(['b@2026-10-05 14:34:51', 'c@2026-10-05 12:00:00', 'a@2026-10-05 10:03:55']), JSON.stringify(rep));
    const legendTimes = (await page.textContent('#compareLegend')).match(/\d{4}-\d\d-\d\d.\d\d:\d\d:\d\d/g) || [];
    check('timestamps: the legend shows every run in the same format', legendTimes.length === 3 && legendTimes.every(t => t[10] === ' '), JSON.stringify(legendTimes));

    // ...and a scheduled run arriving from the server really is converted by the sync path
    await page.route('**/api/scheduler-history', route => route.fulfill({ contentType: 'application/json', body: JSON.stringify({
      ok: true, runs: [{ job_id: 'sched-ts-test', started: '2026-10-05T10:03:55Z', status: 'done', schedule_name: 'ts', args: { server: '9.9.9.9', protocol: 'udp' }, lines: ['1,000 q/s'] }] }) }));
    const synced = await page.evaluate(async () => { await syncSchedulerHistory(); const r = runHistory.find(x => x.id === 'sched-ts-test'); return r && r.started; });
    await page.unroute('**/api/scheduler-history');
    check('timestamps: a scheduled run from the server is stored as "YYYY-MM-DD HH:MM:SS"', synced === '2026-10-05 10:03:55', String(synced));


    // The winning slice is featured: throughput -> the largest, p95 -> the smallest
    const geom = await page.evaluate(() => {
      // paint [3,1] twice into scratch canvases and measure how far each slice reaches from the centre
      const reach = (winners) => {
        const host = document.createElement('div'); host.style.cssText = 'position:fixed;left:0;top:0;width:300px;height:300px;z-index:-1';
        const cv = document.createElement('canvas'); host.appendChild(cv); document.body.appendChild(host);
        const bg = '#101010', ch = DnsCharts.pie(cv, { values: [3, 1], colors: ['#4477ff', '#ff7744'], theme: { text: '#ffffff', bg }, winners });
        const ctx = cv.getContext('2d'), dpr = cv.width / 300;
        const isBg = (x, y) => { const d = ctx.getImageData(Math.round(x * dpr), Math.round(y * dpr), 1, 1).data; return Math.abs(d[0] - 16) + Math.abs(d[1] - 16) + Math.abs(d[2] - 16) < 24; };
        const ray = deg => { const a = deg * Math.PI / 180; let last = 0; for (let r = 5; r < 148; r += 1) { if (!isBg(150 + Math.cos(a) * r, 150 + Math.sin(a) * r)) last = r; } return last; };
        const out = { big: ray(45), small: ray(225) };  // mid-angles of the 75% and 25% slices
        ch.destroy(); host.remove(); return out;
      };
      return { plain: reach([]), smallWins: reach([1]), bigWins: reach([0]) };
    });
    check('pies: with no winner both slices reach the same distance', Math.abs(geom.plain.big - geom.plain.small) <= 3, JSON.stringify(geom.plain));
    check('pies: a winning small slice reaches clearly farther out than the other', geom.smallWins.small - geom.smallWins.big >= 12, JSON.stringify(geom.smallWins));
    check('pies: a winning big slice reaches clearly farther out than the other', geom.bigWins.big - geom.bigWins.small >= 12, JSON.stringify(geom.bigWins));

    const sweepTips = async id => {
      const b = await page.locator('#' + id).boundingBox(), cx = b.x + b.width / 2, cy = b.y + b.height / 2, R = Math.min(b.width, b.height) / 2 - 40;
      const seen = new Set();
      for (const f of [0.35, 0.7]) for (let ang = 0; ang < 360; ang += 4) {
        await page.mouse.move(cx + Math.cos(ang * Math.PI / 180) * R * f, cy + Math.sin(ang * Math.PI / 180) * R * f);
        const t = await page.$('#' + id + ' ~ [role=tooltip]');   // this chart's own tooltip (each pie has one)
        if (t && await t.isVisible()) seen.add((await t.innerText()).replace(/\s+/g, ' '));
      }
      await page.mouse.move(5, 5);
      return [...seen];
    };
    const setRuns = rows => page.evaluate(rows => {
      runHistory.length = 0;
      rows.forEach((r, i) => runHistory.push({ started: '2026-10-05 09:0' + i + ':00', _selected: true, args: { server: r[0], protocol: 'udp' }, stats: { qps: r[1], p95: r[2], errors: 0, sent: 1000 } }));
      compareSelected();
    }, rows);
    // A is fastest, B has the lowest latency: different winners in the two pies
    await setRuns([['10.0.0.1', 3000, 50], ['10.0.0.2', 1000, 5]]);
    await page.waitForTimeout(300);
    const qTips = (await sweepTips('compareQpsCanvas')).filter(t => t.includes('Highest throughput'));
    const pTips = (await sweepTips('compareP95Canvas')).filter(t => t.includes('Lowest p95'));
    check('pies: the throughput pie features the highest-throughput run', qTips.length === 1 && qTips[0].includes('10.0.0.1'), JSON.stringify(qTips));
    check('pies: the p95 pie features the LOWEST-latency run', pTips.length === 1 && pTips[0].includes('10.0.0.2'), JSON.stringify(pTips));
    // A tie features nothing (but p95 still differs)
    await setRuns([['10.0.0.1', 2000, 50], ['10.0.0.2', 2000, 5]]);
    await page.waitForTimeout(300);
    const tieQ = (await sweepTips('compareQpsCanvas')).filter(t => t.includes('Highest throughput'));
    const tieP = (await sweepTips('compareP95Canvas')).filter(t => t.includes('Lowest p95'));
    check('pies: a tie features no slice, while the other pie still does', tieQ.length === 0 && tieP.length === 1, JSON.stringify([tieQ, tieP]));

    // Live comparison: once open it follows the ticks; no second click on Compare
    await page.evaluate(() => {
      closeCompare(); runHistory.length = 0;
      [['10.0.0.1', 4000, 5], ['10.0.0.2', 3000, 8], ['10.0.0.3', 2000, 12], ['10.0.0.4', 1000, 20]].forEach((r, i) =>
        runHistory.push({ started: '2026-10-05 08:0' + i + ':00', args: { server: r[0], protocol: 'udp' }, stats: { qps: r[1], p95: r[2], errors: 0, sent: 1000 } }));
      renderHistory(); updateToolbar();
    });
    const tick = n => page.locator('#historyList input[type=checkbox]').nth(n).click();
    const names4 = () => page.locator('#compareLegend .pie-legend-name').allTextContents().then(a => a.map(t => t.split(' ')[0]));
    const swatches = () => page.evaluate(() => Object.fromEntries([...document.querySelectorAll('#compareLegend .pie-legend-row')].map(r => [r.querySelector('.pie-legend-name').textContent.split(' ')[0], r.querySelector('.pie-swatch').style.background])));
    const panelShown = () => page.evaluate(() => !document.getElementById('compareChart').classList.contains('d-none'));
    const btnShown = () => page.evaluate(() => !document.getElementById('btnCompare').classList.contains('d-none'));
    await tick(0);
    check('live: with one ticked there is no Compare button and no panel', !(await btnShown()) && !(await panelShown()));
    await tick(1);
    check('live: with two ticked the Compare button appears and the panel is still closed', (await btnShown()) && !(await panelShown()));
    await page.click('#btnCompare');
    check('live: clicking Compare opens the panel with the two runs and the button steps aside',
      (await panelShown()) && !(await btnShown()) && JSON.stringify(await names4()) === JSON.stringify(['10.0.0.1', '10.0.0.2']), JSON.stringify(await names4()));
    await tick(2);
    check('live: ticking a third run adds it without clicking Compare', JSON.stringify(await names4()) === JSON.stringify(['10.0.0.1', '10.0.0.2', '10.0.0.3']), JSON.stringify(await names4()));
    await tick(3);
    check('live: ticking a fourth adds that too', JSON.stringify(await names4()) === JSON.stringify(['10.0.0.1', '10.0.0.2', '10.0.0.3', '10.0.0.4']), JSON.stringify(await names4()));
    const sw4 = await swatches();
    check('live: all four runs have different colours', new Set(Object.values(sw4)).size === 4, JSON.stringify(sw4));
    const livePaint = await painted('compareQpsCanvas');
    check('live: the redrawn pie is actually painted', livePaint.diff > 200, JSON.stringify(livePaint));
    await tick(1);
    const sw3 = await swatches();
    check('live: unticking one removes it from the comparison', JSON.stringify(await names4()) === JSON.stringify(['10.0.0.1', '10.0.0.3', '10.0.0.4']), JSON.stringify(await names4()));
    check('live: the runs that stay keep their colours', sw3['10.0.0.1'] === sw4['10.0.0.1'] && sw3['10.0.0.3'] === sw4['10.0.0.3'] && sw3['10.0.0.4'] === sw4['10.0.0.4'], JSON.stringify([sw4, sw3]));
    await tick(1);
    const swBack = await swatches();
    check('live: a run ticked again takes the freed colour, and the others still keep theirs', swBack['10.0.0.2'] === sw4['10.0.0.2'] && swBack['10.0.0.1'] === sw4['10.0.0.1'], JSON.stringify([sw4, swBack]));
    await tick(0); await tick(1); await tick(2);
    check('live: with only one run left the panel hides itself', !(await panelShown()) && !(await btnShown()));
    await tick(1);
    check('live: ticking back up to two brings the comparison back without a click', (await panelShown()) && JSON.stringify(await names4()) === JSON.stringify(['10.0.0.2', '10.0.0.4']), JSON.stringify(await names4()));
    await page.click('#compareChart button');
    check('live: the X closes it and clears the ticks', !(await panelShown()) && (await page.locator('#historyList input[type=checkbox]:checked').count()) === 0);
    await tick(0); await tick(1);
    check('live: after closing, ticking two does not reopen it; Compare is offered again', !(await panelShown()) && (await btnShown()));
    await page.evaluate(() => closeCompare());
    await page.evaluate(() => closeCompare());
    check('charts: closing removes the tooltip', (await page.locator('[role=tooltip]').count()) === 0);


    // Updates page: upload a release, see it staged, and the confirmation before installing.
    // (The install itself restarts the daemon, so it is covered by update.js instead.)
    await page.evaluate(() => showPage('updates', document.querySelector('.nav-link[onclick*="updates"]')));
    await page.waitForSelector('#updStats .upd-stat');
    const api = await page.evaluate(async () => (await (await fetch('/api/update')).json()).data);
    const running = api.running;
    check('updates: the page shows the running version', (await page.textContent('#updStats')).includes('v' + running), await page.textContent('#updStats'));
    check('updates: the Upload and Update buttons exist', (await page.locator('#updUploadBtn').count()) === 1 && (await page.locator('#updApplyBtn').count()) === 1);
    // a file that is not a release is refused with the reason, and nothing changes
    const stagedBefore = api.source_version;
    await page.setInputFiles('#updFile', { name: 'notes.tgz', mimeType: 'application/gzip', buffer: Buffer.from('this is not an archive') });
    await page.click('#updUploadBtn');
    await page.waitForFunction(() => document.getElementById('updMsg').textContent.includes('rejected'), null, { timeout: 10000 });
    check('updates: an invalid archive is refused with a reason', (await page.textContent('#updMsg')).includes('not a .tgz'), await page.textContent('#updMsg'));
    check('updates: a refused upload stages nothing', (await page.evaluate(async () => (await (await fetch('/api/update')).json()).data.source_version)) === stagedBefore);
    // a release that is not newer is refused
    const mkRelease = (ver) => {
      const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'dnsbench-rel-'));
      const root = path.join(dir, 'dnsbench');
      fs.mkdirSync(path.join(root, 'source'), { recursive: true });
      fs.writeFileSync(path.join(root, 'README.md'), '# test release\n');
      fs.writeFileSync(path.join(root, 'source', 'go.mod'), 'module dnsbench\n\ngo 1.22\n');
      fs.writeFileSync(path.join(root, 'source', 'main.go'), 'package main\n\nfunc main() {}\n');
      fs.writeFileSync(path.join(root, 'source', 'VERSION'), ver + '\n');
      const out = path.join(dir, 'dnsbench_v' + ver + '.tgz');
      execFileSync('tar', ['czf', out, '-C', dir, 'dnsbench']);
      return out;
    };
    await page.setInputFiles('#updFile', mkRelease(running));
    await page.click('#updUploadBtn');
    await page.waitForFunction(() => document.getElementById('updMsg').textContent.includes('not newer'), null, { timeout: 10000 });
    check('updates: a release that is not newer is refused', true);
    // a newer one is staged
    const newer = String(Number(running) + 1);
    await page.setInputFiles('#updFile', mkRelease(newer));
    await page.click('#updUploadBtn');
    await page.waitForFunction(() => document.getElementById('updMsg').textContent.includes('is staged'), null, { timeout: 10000 });
    await page.waitForFunction(v => document.getElementById('updStats').textContent.includes('v' + v), newer, { timeout: 10000 });
    check('updates: the staged version is shown', /Staged source\s*v/.test(await page.textContent('#updStats')) && (await page.textContent('#updStats')).includes('v' + newer), await page.textContent('#updStats'));
    const afterApi = await page.evaluate(async () => (await (await fetch('/api/update')).json()).data);
    const canApply = afterApi.enabled && afterApi.problems.length === 0;
    check('updates: the Update button is enabled exactly when nothing blocks an update (' + (canApply ? 'nothing does' : afterApi.problems.join(' | ')) + ')',
      (await page.isDisabled('#updApplyBtn')) === !canApply, JSON.stringify(afterApi.problems));
    if (canApply) {
      check('updates: the button names the version', (await page.textContent('#updApplyBtn')).includes('v' + newer));
      // the confirmation says what will happen; declining it sends nothing
      let applyRequests = 0;
      page.on('request', r => { if (r.url().endsWith('/api/update/apply')) applyRequests++; });
      await page.evaluate(() => { window.__confirms = []; window.__origConfirm = window.confirm; window.confirm = m => { window.__confirms.push(m); return false; }; });
      await page.click('#updApplyBtn');
      const asked = await page.evaluate(() => { const c = window.__confirms.slice(); window.confirm = window.__origConfirm; return c; });
      check('updates: Update now asks first, and says it restarts, stops benchmarks and signs everyone out',
        asked.length === 1 && /restart/i.test(asked[0]) && /benchmark/i.test(asked[0]) && /sign in/i.test(asked[0]) && /rolled back/i.test(asked[0]), JSON.stringify(asked));
      check('updates: declining the confirmation sends no request', applyRequests === 0);
    }
    const histText = await page.textContent('#updHistory');
    check('updates: the history lists the upload with who did it', histText.includes('uploaded') && histText.includes('v' + newer) && histText.includes('(by ' + USER + ')'), histText.replace(/\s+/g, ' ').slice(0, 300));
    // the polling stops when you leave the page
    await page.evaluate(() => showPage('bench', document.querySelector('.nav-link[onclick*="bench"]')));
    check('updates: polling stops on leaving the page', await page.evaluate(() => _updTimer === null));

    // Docs pages
    await page.evaluate(() => showPage('readme', document.querySelector('.nav-link[onclick*="readme"]')));
    await page.waitForFunction(() => document.getElementById('readmeContent').textContent.length > 200, null, { timeout: 10000 });
    check('readme page loads', true);
    await page.evaluate(() => showPage('license', document.querySelector('.nav-link[onclick*="license"]')));
    await page.waitForFunction(() => document.getElementById('licenseContent').textContent.includes('GNU GENERAL PUBLIC LICENSE'), null, { timeout: 10000 });
    check('license page loads', true);

    // the 401 is the deliberate wrong-password attempt; the two 422s are the deliberate refused uploads (exactly two)
    let expected422 = 2;
    const real = errors.filter(e => {
      if (e.includes('401')) return false;
      if (e.includes('422') && expected422 > 0) { expected422--; return false; }
      return true;
    });
    check('updates: both refused uploads were answered 422 and nothing else was', expected422 === 0);
    check('no JS or resource errors in the browser', real.length === 0, real.join(' | '));
    check('nothing was requested from outside the server', external.size === 0, [...external].join(' '));
    await ctx.close();
  }
  await browser.close();
  console.log(failures ? `\n${failures} FAILED` : '\nALL UI CHECKS PASSED');
  process.exit(failures ? 1 : 0);
})();
