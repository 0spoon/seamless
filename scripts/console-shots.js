// Captures the console screenshots for the landing page (docs/static/shots/).
// Drives the locally installed Chrome via playwright-core; no bundled browser.
//
//   pnpm add playwright-core          # once, anywhere; or run from a dir that has it
//   SEAMLESS_SHOT_BASE=http://127.0.0.1:8090 SEAMLESS_MCP_API_KEY=<key> \
//     node scripts/console-shots.js /tmp/shots
//
// Point it at a THROWAWAY instance seeded by cmd/demoseed, never a live one.
// Convert the PNGs with `cwebp -q 84` into docs/static/shots/.
const { createHash } = require('node:crypto');
const path = require('node:path');
const { chromium } = require('playwright-core');

const BASE = process.env.SEAMLESS_SHOT_BASE || 'http://127.0.0.1:8090';
const KEY = process.env.SEAMLESS_MCP_API_KEY;
if (!KEY) {
  console.error('SEAMLESS_MCP_API_KEY is required (the throwaway instance key)');
  process.exit(1);
}
// Mirrors internal/console consoleToken: the cookie holds a digest, not the key.
const COOKIE = createHash('sha256').update(`seamless-console\0${KEY}`).digest('hex');

const PAGES = [
  { name: 'overview', url: '/console/?w=30d' },
  { name: 'interactions', url: '/console/interactions' },
  { name: 'plans', url: '/console/plans?w=30d' },
  { name: 'retrieval', url: '/console/retrieval?w=30d' },
  { name: 'relations', url: '/console/relations?scope=project&project=orbital' },
];

(async () => {
  const outDir = process.argv[2] || '.';
  const browser = await chromium.launch({ channel: 'chrome', headless: true });
  for (const theme of ['dark', 'light']) {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      deviceScaleFactor: 2,
      colorScheme: theme,
    });
    await context.addCookies([{ name: 'seamless_console', value: COOKIE, url: BASE }]);
    // The console reads its theme from localStorage before first paint.
    await context.addInitScript((t) => {
      try { localStorage.setItem('seamless-theme', t); } catch (e) {}
    }, theme);
    const page = await context.newPage();
    for (const p of PAGES) {
      // 'load', not 'networkidle': the console's SSE stream never goes idle.
      await page.goto(BASE + p.url, { waitUntil: 'load' });
      await page.waitForTimeout(1500);
      await page.screenshot({ path: path.join(outDir, `${p.name}-${theme}.png`) });
      console.log(`captured ${p.name}-${theme}.png`);
    }
    await context.close();
  }
  await browser.close();
})().catch((err) => { console.error(err); process.exit(1); });
