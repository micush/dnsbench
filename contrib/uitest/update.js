// End-to-end test of updating dnsbench from its web UI, with a real restart (Chromium via Playwright).
//
// ui.js stops short of the install because it restarts the daemon it is testing; this script does
// the whole thing: upload a newer release, press Update, watch the page ride out the restart, sign
// in again, and check the new version is running and the update is recorded.
//
// Needs everything ui.js needs, plus:
//   * the daemon started under something that restarts it when it exits, as systemd does
//     (Restart=on-failure); the update re-execs in place, but a rollback relies on a restart;
//   * a release archive newer than the running version: RELEASE=/path/dnsbench_vN.tgz, and
//     EXPECT=N, the version it contains;
//   * Go, a C compiler and the PAM headers on the host the daemon runs on, which is what the
//     update builds with (the Updates page says if anything is missing).
//
//   PW=$(npm root -g)/playwright CHROME=/path/to/chrome BASE=http://127.0.0.1:8453 \
//   UI_USER=benchuser UI_PASS=secret RELEASE=/tmp/dnsbench_v8.tgz EXPECT=8 node update.js
//
// CONFIRM_WAIT (seconds, default 75) is how long to wait for the "applied" entry: the daemon writes
// it once the new version has stayed up for 60 s.
const { chromium } = require(process.env.PW);
const BASE = process.env.BASE;
const USER = process.env.UI_USER || 'benchuser';
const PASS = process.env.UI_PASS || 'Sup3rSecret!';
const RELEASE = process.env.RELEASE;
const EXPECT = process.env.EXPECT;
const CONFIRM_WAIT = Number(process.env.CONFIRM_WAIT || 75) * 1000;
let failures = 0;
const check = (name, ok, extra) => { console.log((ok ? 'PASS ' : 'FAIL ') + name + (ok || !extra ? '' : '  -> ' + extra)); if (!ok) failures++; };

(async () => {
  if (!RELEASE || !EXPECT) { console.log('set RELEASE and EXPECT'); process.exit(2); }
  const browser = await chromium.launch({ executablePath: process.env.CHROME, args: ['--no-sandbox'] });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 1000 } });
  const page = await ctx.newPage();
  const dialogs = [];
  page.on('dialog', d => { dialogs.push(d.message()); d.accept(); });
  const signIn = async () => {
    await page.goto(BASE + '/login');
    await page.fill('[name=username]', USER);
    await page.fill('[name=password]', PASS);
    await Promise.all([page.waitForURL('**/bench'), page.click('button[type=submit]')]);
  };
  const openUpdates = async () => {
    await page.evaluate(() => showPage('updates', document.querySelector('.nav-link[onclick*="updates"]')));
    await page.waitForSelector('#updStats .upd-stat');
  };

  await signIn();
  await openUpdates();
  const before = await page.evaluate(async () => (await (await fetch('/api/update')).json()).data);
  check('the daemon is running an older version than the release', Number(before.running) < Number(EXPECT), before.running + ' vs ' + EXPECT);
  check('nothing blocks an update on this host', before.enabled && before.problems.length === 0, JSON.stringify(before.problems));

  await page.setInputFiles('#updFile', RELEASE);
  await page.click('#updUploadBtn');
  await page.waitForFunction(() => document.getElementById('updMsg').textContent.includes('is staged'), null, { timeout: 30000 });
  await page.waitForFunction(v => document.getElementById('updStats').textContent.includes('v' + v), EXPECT, { timeout: 10000 });
  check('the release is staged', true);

  const t0 = Date.now();
  await page.click('#updApplyBtn');
  await page.waitForFunction(() => document.getElementById('updMsg').textContent.includes('Building v'), null, { timeout: 10000 });
  check('pressing Update asked first', dialogs.length === 1 && /restart/i.test(dialogs[0]), JSON.stringify(dialogs));
  check('the page says it is building', (await page.textContent('#updMsg')).includes('Building v' + EXPECT));
  // the build runs for a minute or two, then the server restarts and forgets every session: the page must notice and open the sign-in page by itself
  await page.waitForURL('**/login', { timeout: 6 * 60 * 1000 });
  console.log('   restarted after ' + Math.round((Date.now() - t0) / 1000) + ' s');
  check('the page rode out the restart and opened the sign-in page by itself', page.url().endsWith('/login'));

  await signIn();
  await openUpdates();
  const after = await page.evaluate(async () => (await (await fetch('/api/update')).json()).data);
  check('the new version is running', after.running === EXPECT, after.running);
  check('the page says it is up to date', (await page.textContent('#updStats')).includes('up to date'), await page.textContent('#updStats'));
  check('the in-app ReadMe was refreshed from the new release', await page.evaluate(async () => (await (await fetch('/api/readme-html')).json()).html.includes('Updating from the web UI')));
  const hist = (await page.textContent('#updHistory')).replace(/\s+/g, ' ');
  check('the history says who uploaded it', hist.includes('uploaded') && hist.includes('(by ' + USER + ')'), hist.slice(0, 200));

  // the daemon confirms the update once the new version has stayed up for a minute
  const deadline = Date.now() + CONFIRM_WAIT;
  let applied = false;
  while (Date.now() < deadline && !applied) {
    const d = await page.evaluate(async () => (await (await fetch('/api/update')).json()).data);
    applied = d.history.some(e => e.kind === 'applied' && e.to === EXPECT && e.from === before.running);
    if (!applied) await page.waitForTimeout(3000);
  }
  check('the update is confirmed as applied after the new version stays up', applied);

  await browser.close();
  console.log(failures ? failures + ' FAILED' : 'ALL UPDATE CHECKS PASSED');
  process.exit(failures ? 1 : 0);
})().catch(e => { console.log('FAIL exception: ' + e.stack); process.exit(1); });
