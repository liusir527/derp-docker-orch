// Run with Playwright installed: node --test internal/api/web/redeem.test.cjs
// Uses installed Chrome by default; set DERP_TEST_BROWSER=chromium for Playwright's browser.
// Every page request is intercepted: no real server, database, or Docker is used.
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync, mkdirSync } = require('node:fs');
const { join } = require('node:path');
const { chromium } = require('playwright');

const html = readFileSync(join(__dirname, 'redeem.html'), 'utf8');
const regions = [
  { id: 'wh', name: '华中', baseDomain: 'wh.example.test' },
  { id: 'cd', name: '西南', baseDomain: 'cd.example.test' },
];
let browser;
before(async () => {
  browser = await chromium.launch({
    headless: true,
    ...(process.env.DERP_TEST_BROWSER === 'chromium' ? {} : { channel: 'chrome' }),
  });
});
after(async () => { await browser?.close(); });

async function fixture(t, options = {}) {
  const context = await browser.newContext({ viewport: options.viewport || { width: 1280, height: 1100 } });
  t.after(() => context.close());
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  t.after(() => assert.deepEqual(errors, [], 'page must not throw JavaScript errors'));
  const state = { delays: { wh: 20, cd: 140 }, posts: [], probes: [], listStatus: 200, list: regions, ...options };
  await page.route('**/*', async route => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.origin === 'http://derp.test' && url.pathname === '/redeem') {
      return route.fulfill({ contentType: 'text/html', body: html });
    }
    if (url.origin === 'http://derp.test' && url.pathname === '/v1/regions') {
      return route.fulfill({ status: state.listStatus, json: { ok: state.listStatus === 200, data: { regions: state.list } } });
    }
    if (url.origin === 'http://derp.test' && url.pathname === '/v1/redeem/redeem') {
      assert.equal(request.method(), 'POST');
      state.posts.push(request.postDataJSON());
      return route.fulfill({ status: state.redeemStatus || 200, json: state.redeemBody || {
        ok: true, data: {
          instance: { subdomain: 'test.relay.example.test', name: '测试中继', createdAt: new Date().toISOString() },
          region: { name: '西南' }, derpMap: { Regions: {} }, secondsRemaining: 3600,
        },
      } });
    }
    const region = regions.find(r => url.hostname === 'relay.' + r.baseDomain);
    if (region && url.pathname === '/derp/latency-check') {
      assert.equal(request.method(), 'GET');
      state.probes.push(request);
      const delay = state.delays[region.id];
      if (delay === 'timeout') return; // Leave the request pending until the page aborts it.
      if (delay === null) return route.abort('failed');
      await new Promise(resolve => setTimeout(resolve, delay));
      return route.fulfill({ status: 200, body: '' });
    }
    throw new Error('Unexpected request (blocked): ' + request.url());
  });
  await page.goto('http://derp.test/redeem');
  return { page, state };
}

async function measured(page) {
  await page.waitForFunction(() => !document.getElementById('btn-remeasure').disabled);
}

test('lowest latency is prominent, never preselected; explicit choice is required and submitted', async t => {
  const { page, state } = await fixture(t);
  await measured(page);
  assert.match(await page.locator('#recommendation-title').innerText(), /华中 DERP.*ms/);
  assert.equal(await page.locator('.recommended .rc-rec').innerText(), '★ 推荐 · 本次延迟最低');
  assert.equal(await page.locator('input[name=region]:checked').count(), 0);
  assert.equal(state.probes.length, 6);
  for (const probe of state.probes) {
    assert.equal(probe.headers().referer, undefined);
    assert.equal(probe.headers().cookie, undefined);
  }
  await page.locator('#code').fill('DERP-TEST-CODE');
  await page.locator('#btn-redeem').click();
  assert.match(await page.locator('#redeem-msg').innerText(), /请先勾选/);
  assert.equal(state.posts.length, 0);
  await page.locator('#btn-pick-recommended').click();
  assert.equal(await page.locator('input[name=region]:checked').inputValue(), 'wh');
  await page.getByRole('radio', { name: '西南 DERP' }).check();
  await page.locator('#btn-redeem').click();
  await page.locator('#result-card').waitFor({ state: 'visible' });
  assert.deepEqual(state.posts, [{ code: 'DERP-TEST-CODE', region: 'cd' }]);
});

test('manual choice during measurement and later remeasurement is preserved', async t => {
  const { page, state } = await fixture(t, { delays: { wh: 100, cd: 200 } });
  await page.getByRole('radio', { name: '西南 DERP' }).check();
  await measured(page);
  assert.equal(await page.locator('input[name=region]:checked').inputValue(), 'cd');
  state.delays = { wh: 180, cd: 20 };
  await page.locator('#btn-remeasure').click();
  assert.equal(await page.locator('#btn-pick-recommended').isVisible(), false);
  await measured(page);
  assert.match(await page.locator('#recommendation-title').innerText(), /西南 DERP/);
  assert.equal(await page.locator('input[name=region]:checked').inputValue(), 'cd');
  assert.match(await page.locator('#region-hint').innerText(), /已选择：西南/);
});

test('all probes failing produces no recommendation, and manual selection remains usable', async t => {
  const { page, state } = await fixture(t, { delays: { wh: null, cd: null } });
  await measured(page);
  assert.match(await page.locator('#recommendation-title').innerText(), /无法给出延迟推荐/);
  assert.equal(await page.locator('.recommended').count(), 0);
  assert.equal(await page.locator('#btn-pick-recommended').isVisible(), false);
  assert.equal(await page.locator('input[name=region]:checked').count(), 0);
  await page.getByRole('radio', { name: '华中 DERP' }).check();
  state.delays = { wh: 20, cd: 140 };
  await page.locator('#btn-remeasure').click();
  await measured(page);
  assert.match(await page.locator('#recommendation-title').innerText(), /华中 DERP/);
  assert.equal(await page.locator('input[name=region]:checked').inputValue(), 'wh');
});

test('unreachable regions are excluded and partial failure is explained', async t => {
  const { page } = await fixture(t, { delays: { wh: null, cd: 20 } });
  await measured(page);
  assert.match(await page.locator('#recommendation-title').innerText(), /西南 DERP/);
  assert.match(await page.locator('#recommendation-detail').innerText(), /1 个地区未测得延迟/);
  assert.equal(await page.locator('.rc-lat.bad').count(), 1);
});

test('hanging probes time out and do not prevent a recommendation or retry', { timeout: 20000 }, async t => {
  const { page } = await fixture(t, { delays: { wh: 'timeout', cd: 20 } });
  await page.waitForFunction(() => !document.getElementById('btn-remeasure').disabled, null, { timeout: 16000 });
  assert.match(await page.locator('#recommendation-title').innerText(), /西南 DERP/);
  assert.equal(await page.locator('.rc-lat.bad').count(), 1);
  assert.equal(await page.locator('input[name=region]:checked').count(), 0);
});

for (const empty of [false, true]) {
  test((empty ? 'empty' : 'failed') + ' region list blocks silent fallback and can be reloaded', async t => {
    const { page, state } = await fixture(t, empty ? { list: [] } : { listStatus: 503 });
    await measured(page);
    assert.match(await page.locator('#recommendation-title').innerText(), /无法加载/);
    await page.locator('#code').fill('DERP-TEST-CODE');
    await page.locator('#btn-redeem').click();
    assert.equal(state.posts.length, 0);
    state.listStatus = 200;
    state.list = regions.slice(0, 1);
    await page.locator('#btn-remeasure').click();
    await measured(page);
    assert.equal(await page.getByRole('radio').count(), 1);
    assert.equal(await page.locator('input[name=region]:checked').count(), 0);
    assert.match(await page.locator('#recommendation-title').innerText(), /华中 DERP/);
  });
}

test('radio cards work with keyboard; desktop and narrow mobile layouts have large targets', async t => {
  const { page } = await fixture(t);
  await measured(page);
  const box = await page.locator('.region-card').first().boundingBox();
  assert.ok(box.width >= 300 && box.height >= 180);
  await page.getByRole('radio', { name: '华中 DERP' }).focus();
  await page.keyboard.press('Space');
  await page.keyboard.press('ArrowRight');
  assert.equal(await page.locator('input[name=region]:checked').inputValue(), 'cd');
  if (process.env.DERP_UI_SCREENSHOTS) {
    mkdirSync(process.env.DERP_UI_SCREENSHOTS, { recursive: true });
    await page.screenshot({ path: join(process.env.DERP_UI_SCREENSHOTS, 'redeem-desktop.png'), fullPage: true });
  }
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
    const mobile = await page.locator('.region-card').first().boundingBox();
    assert.ok(mobile.width >= 230 && mobile.height >= 160);
    await page.getByRole('radio', { name: '华中 DERP' }).check();
    assert.equal(await page.locator('input[name=region]:checked').inputValue(), 'wh');
    if (width === 390 && process.env.DERP_UI_SCREENSHOTS) {
      await page.screenshot({ path: join(process.env.DERP_UI_SCREENSHOTS, 'redeem-mobile.png'), fullPage: true });
    }
  }
});

test('expired code reports an error, keeps selection, and re-enables redemption', async t => {
  const { page } = await fixture(t, { redeemStatus: 410, redeemBody: { ok: false, error: '兑换码已过期' } });
  await measured(page);
  await page.getByRole('radio', { name: '华中 DERP' }).check();
  await page.locator('#code').fill('DERP-EXPIRED');
  await page.locator('#btn-redeem').click();
  await page.waitForFunction(() => !document.getElementById('btn-redeem').disabled);
  assert.equal(await page.locator('#redeem-msg').innerText(), '兑换码已过期');
  assert.equal(await page.locator('#result-card').isVisible(), false);
  assert.equal(await page.locator('input[name=region]:checked').inputValue(), 'wh');
});
