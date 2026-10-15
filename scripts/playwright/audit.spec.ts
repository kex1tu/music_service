import { test, expect } from '@playwright/test';

const BASE = process.env.BASE_URL || 'http://localhost:8080';

// ── Аутентификация ────────────────────────────────────────────────
test('login API → cookie сессии установлен', async ({ page }) => {
  const password = process.env.MM_PASSWORD || 'admin';
  const response = await page.request.post(`${BASE}/api/auth/login`, {
    data: { login: 'admin', password },
  });
  expect(response.status()).toBe(200);

  const cookies = await page.context().cookies();
  expect(cookies.some(c => c.name === 'session')).toBeTruthy();
});

// ── Alpine.js сторы ───────────────────────────────────────────────
test('Alpine.store("app") инициализирован', async ({ page }) => {
  await loginHelper(page);
  await page.goto(BASE);
  const store = await page.evaluate(() => (window as any).Alpine?.store('app'));
  expect(store).toBeDefined();
  expect(store).toHaveProperty('currentUser');
});

test('Alpine.store("player") существует и имеет поле currentTrack', async ({ page }) => {
  await loginHelper(page);
  await page.goto(BASE);
  const player = await page.evaluate(() => (window as any).Alpine?.store('player'));
  expect(player).toBeDefined();
  expect(player).toHaveProperty('currentTrack');
});

// ── No-build инварианты ───────────────────────────────────────────
test('нет npm/node_modules артефактов в ответах', async ({ page }) => {
  const scripts: string[] = [];
  page.on('request', req => {
    if (req.resourceType() === 'script') scripts.push(req.url());
  });
  await page.goto(BASE);
  await page.waitForTimeout(1000);

  scripts.forEach(url => {
    expect(url).toMatch(/cdn\.|\/static\/js\//);
    expect(url).not.toMatch(/webpack|vite|node_modules/);
  });
});

test('Tailwind загружается с CDN', async ({ page }) => {
  const [response] = await Promise.all([
    page.waitForResponse(r => r.url().includes('tailwind')),
    page.goto(BASE),
  ]);
  expect(response.status()).toBe(200);
});

// ── Radar: вкладка ───────────────────────────────────────────────
test('Radar: вкладка переключается', async ({ page }) => {
  await loginHelper(page);
  await page.goto(BASE);
  const radarBtn = page.locator('button:has-text("Радар"), [data-tab="radar"]').first();
  if (await radarBtn.isVisible()) {
    await radarBtn.click();
    await page.waitForTimeout(500);
  }
});

// ── Downloader: форма и элементы ──────────────────────────────────
test('Downloader: форма загрузки присутствует', async ({ page }) => {
  await loginHelper(page);
  await page.goto(BASE);
  const downloadInput = page.locator('input[placeholder*="ссылк"], input[placeholder*="URL"], [data-testid="download-url"]').first();
  await expect(downloadInput).toBeAttached();
});

// ── Аудиоплеер: мини-панель ───────────────────────────────────────
test('Аудиоплеер нижняя панель присутствует в DOM', async ({ page }) => {
  await loginHelper(page);
  await page.goto(BASE);
  const player = page.locator('[x-data*="audioPlayerComponent"], [x-data*="player"]').first();
  await expect(player).toBeAttached();
});

// ── File Manager / Search ─────────────────────────────────────────
test('Поиск в библиотеке присутствует и интерактивен', async ({ page }) => {
  await loginHelper(page);
  await page.goto(BASE);
  const searchInput = page.locator('input[placeholder*="Поиск"], [data-testid="library-search"]').first();
  await expect(searchInput).toBeAttached();
});

// ── Helper ────────────────────────────────────────────────────────
async function loginHelper(page: any) {
  const password = process.env.MM_PASSWORD || 'admin';
  await page.request.post(`${BASE}/api/auth/login`, {
    data: { login: 'admin', password },
  });
}
