import { execFileSync } from 'node:child_process';
import { test, expect, type Page } from '@playwright/test';

const BASE = process.env.BASE_URL || 'http://localhost:8080';

test.beforeEach(async ({ page }) => {
  const response = await page.request.post(`${BASE}/api/auth/login`, {
    data: { login: 'admin', password: process.env.MM_PASSWORD || 'admin' },
  });
  await expect(response).toBeOK();
  await page.goto(BASE);
  await page.waitForFunction(() => !!(window as any).Alpine?.store('player')?.audioElement);
});

async function installControlledAudio(page: Page, held: string[] = []) {
  await page.evaluate(heldSources => {
    const w = window as any;
    const player = w.Alpine.store('player');
    const audio: HTMLAudioElement = player.audioElement;
    const calls: string[] = [];
    const pending = new Map<string, { resolve: () => void; reject: (err: Error) => void }>();
    let src = '';
    let paused = true;
    let currentTime = 0;
    let pauses = 0;

    const fixture = {
      audio,
      player,
      calls,
      ready: 1,
      track(id: string) {
        return { id, path: `${id}.mp3`, title: id, type: 'file', extension: '.mp3', duration: 30, stream_url: `/${id}.mp3` };
      },
      finish(url: string, abort = false) {
        const request = pending.get(url);
        if (!request) throw new Error(`No pending play for ${url}`);
        pending.delete(url);
        if (abort) request.reject(new DOMException('Source changed', 'AbortError'));
        else request.resolve();
      },
      end() {
        paused = true;
        audio.dispatchEvent(new Event('ended'));
      },
      snapshot() {
        return {
          track: player.currentTrack?.id ?? null,
          src,
          paused,
          playing: player.isPlaying,
          loading: player.isLoading,
          position: currentTime,
          index: player.currentIndex,
          queue: player.playlist.map((item: { id: string }) => item.id),
          pauses,
          plays: calls.length,
        };
      },
    };
    const setSource = (value: string) => { src = value; currentTime = 0; paused = true; };
    const removeAttribute = audio.removeAttribute.bind(audio);
    audio.removeAttribute = (name: string) => {
      if (name === 'src') setSource('');
      removeAttribute(name);
    };
    Object.defineProperties(audio, {
      src: {
        get: () => src,
        set: setSource,
      },
      currentTime: { get: () => currentTime, set: (value: number) => { currentTime = value; } },
      paused: { get: () => paused },
      readyState: { get: () => fixture.ready },
    });
    audio.load = () => {};
    audio.play = async () => {
      const requested = src;
      calls.push(requested);
      if (heldSources.includes(requested)) {
        await new Promise<void>((resolve, reject) => pending.set(requested, { resolve, reject }));
      }
      if (requested === src) {
        paused = false;
        audio.dispatchEvent(new Event('play'));
      }
    };
    audio.pause = () => {
      paused = true;
      pauses++;
      audio.dispatchEvent(new Event('pause'));
    };

    // Clock, native events and promise results are controlled on the actual bound element.
    player.ensureAudioContext = () => {};
    const raw = w.api.raw.bind(w.api);
    w.api.raw = (url: string, ...args: unknown[]) => {
      if (url.startsWith('/lyrics?')) return Promise.resolve({ ok: true, json: async () => ({ kind: 'none' }) });
      if (url.endsWith('/metadata')) return Promise.resolve({ ok: true, json: async () => ({}) });
      return raw(url, ...args);
    };
    w.playerFixture = fixture;
  }, held);
}

const snapshot = (page: Page) => page.evaluate(() => (window as any).playerFixture.snapshot());

test('Поздний AbortError предыдущего трека не сбрасывает воспроизведение нового', async ({ page }) => {
  await installControlledAudio(page, ['/A.mp3']);
  await page.evaluate(() => {
    const f = (window as any).playerFixture;
    const tracks = [f.track('A'), f.track('B')];
    f.player.playTrack(tracks[0], tracks);
    f.player.playTrack(tracks[1], tracks);
  });
  await expect.poll(() => snapshot(page)).toMatchObject({ track: 'B', src: '/B.mp3', playing: true });
  await page.evaluate(async () => {
    (window as any).playerFixture.finish('/A.mp3', true);
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
  });
  expect(await snapshot(page)).toMatchObject({ track: 'B', src: '/B.mp3', paused: false, playing: true, loading: false });
});

test('Отложенная перемотка фрагмента не применяется к новому треку', async ({ page }) => {
  await installControlledAudio(page, ['/spot.mp3']);
  await page.evaluate(async () => {
    const f = (window as any).playerFixture;
    f.ready = 0;
    f.player.playCensorshipSpot('/spot.mp3', { start_sec: 10, end_sec: 12, word: 'fixture' });
    const track = f.track('B');
    f.player.playTrack(track, [track]);
    f.audio.dispatchEvent(new Event('loadedmetadata'));
    f.finish('/spot.mp3');
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
  });
  expect(await snapshot(page)).toMatchObject({ track: 'B', src: '/B.mp3', position: 0, playing: true, loading: false });
});

test('Таймер фрагмента аудита не ставит следующий трек на паузу', async ({ page }) => {
  await page.clock.install();
  await installControlledAudio(page);
  await page.evaluate(() => {
    const f = (window as any).playerFixture;
    f.player.playCensorshipSpot('/spot.mp3', { start_sec: 10, end_sec: 12, word: 'fixture' });
  });
  await expect.poll(() => snapshot(page)).toMatchObject({ src: '/spot.mp3', position: 10, playing: true });
  await page.evaluate(() => {
    const f = (window as any).playerFixture;
    const track = f.track('B');
    f.player.playTrack(track, [track]);
  });
  await expect.poll(() => snapshot(page)).toMatchObject({ track: 'B', playing: true });
  await page.clock.runFor(2_100);
  expect(await snapshot(page)).toMatchObject({ track: 'B', paused: false, playing: true, pauses: 0 });
});

test('После удаления последней дорожки очередь остаётся пустой', async ({ page }) => {
  await installControlledAudio(page);
  const state = await page.evaluate(async () => {
    const f = (window as any).playerFixture;
    const track = f.track('A');
    f.player.playTrack(track, [track]);
    await Promise.resolve();
    f.player.removeFromQueue(0);
    const removed = f.snapshot();
    f.player.next();
    return { removed, next: f.snapshot() };
  });
  const empty = { track: null, src: '', index: -1, queue: [], playing: false, plays: 1 };
  expect(state.removed).toMatchObject(empty);
  expect(state.next).toMatchObject(empty);
});

test('Запоздавшие текст и метаданные не подменяют активный трек', async ({ page }) => {
  await installControlledAudio(page);
  const state = await page.evaluate(async () => {
    const w = window as any;
    const f = w.playerFixture;
    const deferred: Array<() => void> = [];
    const raw = w.api.raw.bind(w.api);
    w.api.raw = (url: string, ...args: unknown[]) => {
      const lyrics = url.startsWith('/lyrics?');
      if (!lyrics && !url.endsWith('/metadata')) return raw(url, ...args);
      const old = lyrics ? new URL(url, location.origin).searchParams.get('path') === 'A.mp3' : url.includes('/A/');
      const data = lyrics ? { kind: 'lrc', content: `[00:01.00]${old ? 'Old lyrics' : 'Current lyrics'}` } :
        { id: old ? 'A' : 'B', bitrate_kbps: old ? 128 : 320, genre: old ? 'Old genre' : 'Current genre' };
      const response = { ok: true, json: async () => data };
      if (old) return new Promise(resolve => deferred.push(() => resolve(response)));
      return Promise.resolve(response);
    };
    const tracks = [f.track('A'), f.track('B')];
    const read = () => ({
      track: f.player.currentTrack?.id,
      text: f.player.lyricsLines[0]?.text,
      genre: f.player.techSpecs.genre,
      bitrate: f.player.techSpecs.bitrate,
      loading: f.player.lyricsLoading,
    });
    f.player.playTrack(tracks[0], tracks);
    f.player.playTrack(tracks[1], tracks);
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
    const before = read();
    for (const release of deferred) release();
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
    return { before, after: read(), pending: deferred.length };
  });
  expect(state.pending).toBe(2);
  const active = { track: 'B', text: 'Current lyrics', genre: 'Current genre', bitrate: 320, loading: false };
  expect(state.before).toEqual(active);
  expect(state.after).toEqual(active);
});

test('Повтор после ошибки стриминга заново загружает тот же MP3', async ({ page }) => {
  const mp3 = execFileSync('ffmpeg', [
    '-nostdin', '-v', 'error', '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=44100:duration=3',
    '-codec:a', 'libmp3lame', '-b:a', '320k', '-f', 'mp3', 'pipe:1',
  ]);
  let available = false;
  let requests = 0;
  await page.route('**/player-network-fixture.mp3', async route => {
    requests++;
    if (!available) {
      await route.fulfill({ status: 503 });
      return;
    }
    const range = /^bytes=(\d+)-(\d*)$/.exec(route.request().headers().range || '');
    if (!range) {
      await route.fulfill({ contentType: 'audio/mpeg', body: mp3 });
      return;
    }
    const start = Number(range[1]);
    const end = range[2] ? Math.min(Number(range[2]), mp3.length - 1) : mp3.length - 1;
    if (start > end) {
      await route.fulfill({ status: 416, headers: { 'Content-Range': `bytes */${mp3.length}` } });
      return;
    }
    await route.fulfill({
      status: 206, contentType: 'audio/mpeg',
      headers: { 'Accept-Ranges': 'bytes', 'Content-Range': `bytes ${start}-${end}/${mp3.length}` },
      body: mp3.subarray(start, end + 1),
    });
  });
  await page.route(url => url.pathname === '/lyrics', route => route.fulfill({ json: { kind: 'none' } }));
  await page.route('**/api/tracks/retry/metadata', route => route.fulfill({ json: { id: 'retry' } }));

  await page.evaluate(() => {
    const player = (window as any).Alpine.store('player');
    const track = {
      id: 'retry', path: 'retry.mp3', title: 'Трек для повтора', type: 'file', extension: '.mp3',
      stream_url: '/player-network-fixture.mp3',
    };
    // A click supplies browser user activation on the first play as well as on retry.
    const button = document.createElement('button');
    button.textContent = 'Запустить тестовый трек';
    button.onclick = () => { button.remove(); player.playTrack(track, [track]); };
    document.body.prepend(button);
  });
  await page.getByRole('button', { name: 'Запустить тестовый трек' }).click();
  await expect(page.getByText('Ошибка стриминга аудиофайла', { exact: true })).toBeVisible();
  const audio = page.locator('audio[x-ref="nativeAudio"]');
  await expect.poll(() => audio.evaluate((el: HTMLAudioElement) => el.error?.code ?? 0)).toBeGreaterThan(0);
  const before = requests;
  available = true;
  await page.getByRole('button', { name: 'Начать воспроизведение', exact: true }).click();

  await expect.poll(() => audio.evaluate((el: HTMLAudioElement) => el.currentTime)).toBeGreaterThan(0);
  expect(requests).toBeGreaterThan(before);
  const result = await page.evaluate(() => {
    const player = (window as any).Alpine.store('player');
    return { id: player.currentTrack.id, index: player.currentIndex, playing: player.isPlaying, loading: player.isLoading };
  });
  expect(result).toEqual({ id: 'retry', index: 0, playing: true, loading: false });
});

for (const { mode, track, index, position, plays, playing } of [
  { mode: 'none', track: 'B', index: 1, position: 30, plays: 1, playing: false },
  { mode: 'all', track: 'A', index: 0, position: 0, plays: 2, playing: true },
  { mode: 'one', track: 'B', index: 1, position: 0, plays: 2, playing: true },
]) {
  test(`Конец последней дорожки в режиме повтора ${mode}`, async ({ page }) => {
    await installControlledAudio(page);
    await page.evaluate(async loopMode => {
      const f = (window as any).playerFixture;
      const tracks = [f.track('A'), f.track('B')];
      f.player.playTrack(tracks[1], tracks);
      await Promise.resolve();
      f.player.loopMode = loopMode;
      f.audio.currentTime = 30;
      f.audio.dispatchEvent(new Event('timeupdate'));
      f.end();
    }, mode);
    await expect.poll(() => snapshot(page)).toMatchObject({
      track, index, src: `/${track}.mp3`, position, plays, playing, paused: !playing,
    });
  });
}
