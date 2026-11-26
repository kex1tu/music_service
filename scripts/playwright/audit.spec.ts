import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';

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
  const radarButton = page.getByRole('button', { name: /^Радар(?:\s*\d+)?$/ });
  const radarHeading = page.getByRole('heading', { name: /^Радар релизов/ });

  await expect(radarButton).toBeVisible();
  await expect(radarHeading).toBeHidden();
  await radarButton.click();
  await expect(radarButton).toHaveAttribute('aria-current', 'page');
  await expect(radarHeading).toBeVisible();
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
test('Поиск в библиотеке показывает совпадение и пустую выдачу', async ({ page }) => {
  const artist = 'Тестовый исполнитель';
  const missing = 'Несуществующий исполнитель';
  await page.route(/\/files\/search\?/, async route => {
    const query = new URL(route.request().url()).searchParams.get('query');
    if (query !== artist && query !== missing) {
      await route.fulfill({ status: 400 });
      return;
    }
    const artists = query === artist ? [{ name: artist, folder_path: artist, track_count: 1 }] : [];
    await route.fulfill({ json: { query, artists, albums: [], tracks: [], total_matches: artists.length } });
  });

  await loginHelper(page);
  await page.goto(BASE);
  const searchInput = page.getByRole('textbox', { name: 'Поиск по медиатеке (исполнители, альбомы, треки)' });
  const result = page.getByRole('button', { name: artist });
  const emptyState = page.getByText('Ничего не найдено', { exact: true }).filter({ visible: true });

  await expect(searchInput).toBeVisible();
  await searchInput.fill(artist);
  await expect(result).toBeVisible();

  await searchInput.fill(missing);
  await expect(emptyState).toBeVisible();
  await expect(result).toBeHidden();
});

for (const nextAction of ['new query', 'clear query'] as const) {
  test(`Поиск игнорирует запоздавший ответ после действия: ${nextAction}`, async ({ page }) => {
    const firstArtist = 'Первый исполнитель';
    const secondArtist = 'Второй исполнитель';
    let releaseFirst!: () => void;
    const firstResponseGate = new Promise<void>(resolve => { releaseFirst = resolve; });
    const queryOf = (url: string) => new URL(url).searchParams.get('query');

    await page.route(url => url.pathname === '/files/search', async route => {
      const query = queryOf(route.request().url());
      if (query === firstArtist) await firstResponseGate;
      await route.fulfill({
        json: { query, artists: [{ name: query, track_count: 1 }], albums: [], tracks: [], total_matches: 1 },
      });
    });

    await loginHelper(page);
    await page.clock.install();
    await page.goto(BASE);
    const input = page.getByRole('textbox', { name: 'Поиск по медиатеке (исполнители, альбомы, треки)' });
    const firstResult = page.getByText(firstArtist, { exact: true });
    const secondResult = page.getByRole('button', { name: secondArtist });

    try {
      await Promise.all([
        page.waitForRequest(request => queryOf(request.url()) === firstArtist),
        input.fill(firstArtist),
      ]);
      if (nextAction === 'new query') {
        await input.fill(secondArtist);
        await expect(secondResult).toBeVisible();
      } else {
        await input.fill('');
        await expect(input).toHaveValue('');
        await page.clock.runFor(250);
      }

      const pendingResponse = page.waitForResponse(response => queryOf(response.url()) === firstArtist);
      releaseFirst();
      const response = await pendingResponse;
      await response.finished();
      // Give the browser a render turn after receiving the response body.
      await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));

      await expect(firstResult).toHaveCount(0);
      if (nextAction === 'new query') {
        await expect(input).toHaveValue(secondArtist);
        await expect(secondResult).toBeVisible();
      } else {
        await expect(input).toHaveValue('');
      }
    } finally {
      releaseFirst();
    }
  });
}

test('История аудита сохраняет текущий запуск после ошибки и повторяет загрузку', async ({ page }) => {
  const newest = { id: 'recent', created_at: '2026-11-21T12:00:00Z', state: 'completed', total: 1, done: 1, errors: 0 };
  const older = { ...newest, id: 'older', created_at: '2026-11-20T12:00:00Z' };
  const cursors: string[] = [];

  await page.route(url => url.pathname === '/api/audit/censorship', async route => {
    const cursor = new URL(route.request().url()).searchParams.get('cursor');
    if (cursor === null) {
      await route.fulfill({ json: { runs: [newest], next_cursor: 'fixture-cursor', active: false } });
      return;
    }

    cursors.push(cursor);
    if (cursors.length === 1) {
      await route.fulfill({ status: 500, json: { error: 'Не удалось загрузить историю' } });
      return;
    }
    await route.fulfill({ json: { runs: [older, newest], next_cursor: '' } });
  });
  await page.route(url => url.pathname === '/api/audit/censorship/recent/items', route => route.fulfill({
    json: { items: [{ id: 1, path: 'Текущий трек.mp3', current_path: 'Текущий трек.mp3', state: 'done', result: {} }] },
  }));

  await loginHelper(page);
  await page.goto(BASE);
  await page.getByRole('button', { name: 'Аудит', exact: true }).click();
  await page.getByRole('button', { name: 'Проверка цензуры', exact: true }).click();
  const selectRun = page.getByLabel('Запуск аудита', { exact: true });
  const currentTrack = page.getByRole('heading', { name: 'Текущий трек.mp3' });
  const loadOlder = page.getByRole('button', { name: 'Более ранние запуски' });

  await expect(selectRun).toHaveValue(newest.id);
  await expect(currentTrack).toBeVisible();
  await loadOlder.click();
  await expect(page.getByText('Не удалось загрузить историю', { exact: true })).toBeVisible();
  await expect(selectRun).toHaveValue(newest.id);
  await expect(currentTrack).toBeVisible();
  await expect(loadOlder).toBeEnabled();

  await loadOlder.click();
  await expect(selectRun.locator('option[value="older"]')).toHaveCount(1);
  await expect(selectRun.locator('option[value="recent"]')).toHaveCount(1);
  await expect(selectRun).toHaveValue(newest.id);
  await expect(currentTrack).toBeVisible();
  await expect(loadOlder).toBeHidden();
  expect(cursors).toEqual(['fixture-cursor', 'fixture-cursor']);
});

// ── Helper ────────────────────────────────────────────────────────
async function loginHelper(page: Page) {
  const password = process.env.MM_PASSWORD || 'admin';
  const response = await page.request.post(`${BASE}/api/auth/login`, {
    data: { login: 'admin', password },
  });
  await expect(response).toBeOK();
}
