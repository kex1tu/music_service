"""Сервис текстов песен (Lyrics Service).

Обеспечивает:
1. Поиск синхронизированных текстов (.lrc) через LRCLIB API с фолбэком на несинхронизированные тексты.
2. Поиск несинхронизированных текстов (.txt) через Genius API при отсутствии результатов в LRCLIB.
3. Сохранение текстов строго в виде внешних файлов (.lrc/.txt) рядом с целевым MP3 файлом.
4. Строгое соблюдение инварианта Zero ID3 Lyrics: тексты никогда не вшиваются в ID3-теги трека.
5. Изоляцию всех файловых операций через PathSandbox.resolve().
"""

import logging
import re
from pathlib import Path
from typing import Any

import httpx

from app.core.exceptions import PathTraversalError
from app.core.sandbox import PathSandbox
from app.schemas.lyrics import LyricsResponse, LyricsSearchResult

logger = logging.getLogger("music_manager.services.lyrics")


class LrclibClient:
    """Асинхронный клиент для взаимодействия с публичным API LRCLIB (https://lrclib.net)."""

    BASE_URL = "https://lrclib.net/api"

    def __init__(
        self,
        client: httpx.AsyncClient | None = None,
        http_client: httpx.AsyncClient | None = None,
    ) -> None:
        """Инициализирует клиент с возможностью внедрения httpx.AsyncClient для тестов."""
        self._client = client or http_client

    async def _get(
        self,
        url: str,
        params: dict[str, Any] | None = None,
    ) -> httpx.Response:
        """Выполняет асинхронный HTTP GET запрос."""
        headers = {"User-Agent": "MusicManager/1.0 (https://github.com/)"}
        kwargs: dict[str, Any] = {"headers": headers}
        if params is not None:
            kwargs["params"] = params

        if self._client is not None:
            return await self._client.get(url, **kwargs)

        async with httpx.AsyncClient(timeout=10.0) as client:
            return await client.get(url, **kwargs)

    async def get_lyrics(
        self,
        artist: str,
        title: str,
        album: str | None = None,
        duration: int | None = None,
    ) -> LyricsSearchResult | None:
        """Ищет текст песни в LRCLIB.

        Сначала выполняет точный поиск через GET /api/get. При отсутствии результата (404)
        или отсутствии текстов в ответе выполняет поиск через GET /api/search.
        Возвращает синхронизированный текст при наличии, иначе обычный текст.
        При ошибках сети или статусах ошибки возвращает None, не падая.
        """
        if not artist or not title:
            return None

        # 1. Попытка точного поиска через /api/get
        params: dict[str, Any] = {
            "artist_name": artist,
            "track_name": title,
        }
        if album:
            params["album_name"] = album
        if duration is not None:
            params["duration"] = duration

        try:
            get_url = f"{self.BASE_URL}/get"
            resp = await self._get(get_url, params=params)
            if resp.status_code == 200:
                data = resp.json()
                synced = data.get("syncedLyrics")
                plain = data.get("plainLyrics")
                if synced:
                    return LyricsSearchResult(
                        lyrics=synced,
                        synced=True,
                        source="lrclib",
                        track_name=data.get("trackName") or title,
                        artist_name=data.get("artistName") or artist,
                    )
                if plain:
                    return LyricsSearchResult(
                        lyrics=plain,
                        synced=False,
                        source="lrclib",
                        track_name=data.get("trackName") or title,
                        artist_name=data.get("artistName") or artist,
                    )
            elif resp.status_code != 404:
                logger.warning("LRCLIB /api/get вернул статус %d", resp.status_code)
        except (httpx.HTTPError, Exception) as exc:  # noqa: BLE001
            logger.warning("Ошибка при запросе к LRCLIB /api/get: %s", exc)
            return None

        # 2. Фолбэк на полнотекстовый поиск через /api/search
        try:
            search_url = f"{self.BASE_URL}/search"
            search_params = {"q": f"{artist} {title}".strip()}
            search_resp = await self._get(search_url, params=search_params)
            if search_resp.status_code == 200:
                search_data = search_resp.json()
                if isinstance(search_data, list) and search_data:
                    # Приоритет: сначала ищем элемент с syncedLyrics
                    for item in search_data:
                        if item.get("syncedLyrics"):
                            return LyricsSearchResult(
                                lyrics=item["syncedLyrics"],
                                synced=True,
                                source="lrclib",
                                track_name=item.get("trackName") or title,
                                artist_name=item.get("artistName") or artist,
                            )
                    # Если синхронизированных нет, берем первый с plainLyrics
                    for item in search_data:
                        if item.get("plainLyrics"):
                            return LyricsSearchResult(
                                lyrics=item["plainLyrics"],
                                synced=False,
                                source="lrclib",
                                track_name=item.get("trackName") or title,
                                artist_name=item.get("artistName") or artist,
                            )
            elif search_resp.status_code != 404:
                logger.warning("LRCLIB /api/search вернул статус %d", search_resp.status_code)
        except (httpx.HTTPError, Exception) as exc:  # noqa: BLE001
            logger.warning("Ошибка при запросе к LRCLIB /api/search: %s", exc)
            return None

        return None


class GeniusClient:
    """Асинхронный клиент для взаимодействия с Genius API (https://api.genius.com)."""

    BASE_URL = "https://api.genius.com"

    def __init__(
        self,
        api_token: str | None = None,
        client: httpx.AsyncClient | None = None,
        http_client: httpx.AsyncClient | None = None,
    ) -> None:
        """Инициализирует клиент Genius с опциональным API токеном и HTTP-клиентом."""
        self._api_token = api_token.strip() if api_token else None
        self._client = client or http_client

    async def _get(
        self,
        url: str,
        params: dict[str, Any] | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        """Выполняет асинхронный HTTP GET запрос."""
        req_headers = headers.copy() if headers else {}
        kwargs: dict[str, Any] = {"headers": req_headers}
        if params is not None:
            kwargs["params"] = params

        if self._client is not None:
            return await self._client.get(url, **kwargs)

        async with httpx.AsyncClient(timeout=10.0) as client:
            return await client.get(url, **kwargs)

    async def search_lyrics(
        self,
        artist: str,
        title: str,
    ) -> LyricsSearchResult | None:
        """Ищет текст трека в Genius API.

        Если токен не задан или пуст, возвращает None без ошибок.
        Обрабатывает ошибки сети, таймауты, 401, 403, 404 без падений.
        """
        if not self._api_token:
            return None

        if not artist and not title:
            return None

        url = f"{self.BASE_URL}/search"
        params = {"q": f"{artist} {title}".strip()}
        headers = {"Authorization": f"Bearer {self._api_token}"}

        try:
            resp = await self._get(url, params=params, headers=headers)
            if resp.status_code in (401, 403, 404):
                logger.warning("Genius API вернул статус %d для запроса '%s - %s'", resp.status_code, artist, title)
                return None
            if resp.status_code != 200:
                logger.warning("Genius API вернул неожиданный статус %d", resp.status_code)
                return None

            data = resp.json()
            hits = data.get("response", {}).get("hits", [])
            if not hits:
                return None

            first_hit = hits[0]
            result = first_hit.get("result", {})
            track_name = result.get("title") or title
            primary_artist = result.get("primary_artist")
            artist_name = (
                primary_artist.get("name")
                if isinstance(primary_artist, dict) and primary_artist.get("name")
                else artist
            )

            lyrics_text = result.get("lyrics") or first_hit.get("lyrics")
            if not lyrics_text:
                song_url = result.get("url")
                if song_url:
                    lyrics_text = f"Lyrics available at Genius: {song_url}"
                else:
                    lyrics_text = result.get("full_title") or f"{artist} - {title}"

            return LyricsSearchResult(
                lyrics=lyrics_text,
                synced=False,
                source="genius",
                track_name=track_name,
                artist_name=artist_name,
            )
        except (httpx.HTTPError, Exception) as exc:  # noqa: BLE001
            logger.warning("Ошибка при запросе к Genius API: %s", exc)
            return None


class LyricsService:
    """Сервис управления текстами песен.

    Координирует поиск в LRCLIB и Genius, чтение и сохранение файлов .lrc и .txt
    в файловой системе рядом с целевым MP3-файлом через PathSandbox.
    """

    def __init__(
        self,
        music_sandbox: PathSandbox,
        tmp_sandbox: PathSandbox | None = None,
        lrclib_client: LrclibClient | None = None,
        genius_client: GeniusClient | None = None,
    ) -> None:
        self.music_sandbox = music_sandbox
        self.tmp_sandbox = tmp_sandbox
        self.lrclib_client = lrclib_client or LrclibClient()
        self.genius_client = genius_client or GeniusClient()

    async def search(
        self,
        artist: str,
        title: str,
        album: str | None = None,
        duration: int | None = None,
    ) -> LyricsSearchResult:
        """Выполняет поиск текста песни по каскаду приоритетов.

        Приоритет:
        1. LRCLIB (синхронизированный .lrc) -> немедленный возврат.
        2. LRCLIB (обычный .txt) -> сохраняется как приоритет над Genius.
        3. Genius (обычный .txt) -> фолбэк при отсутствии результатов в LRCLIB.
        4. Если ничего не найдено -> LyricsSearchResult(source='none', lyrics=None).
        """
        lrclib_res = await self.lrclib_client.get_lyrics(
            artist=artist,
            title=title,
            album=album,
            duration=duration,
        )
        if lrclib_res and lrclib_res.lyrics:
            if lrclib_res.synced:
                return lrclib_res
            # LRCLIB plain lyrics beats Genius
            return lrclib_res

        genius_res = await self.genius_client.search_lyrics(
            artist=artist,
            title=title,
        )
        if genius_res and genius_res.lyrics:
            return genius_res

        return LyricsSearchResult(source="none", lyrics=None, synced=False)

    def _resolve_track_path(self, rel_mp3_path: str) -> Path:
        """Безопасно разрешает относительный путь к треку через песочницы.

        Сначала проверяет постоянную медиатеку (music_sandbox). Если файл не существует
        на диске и задан tmp_sandbox, также проверяет временный буфер (tmp_sandbox).
        При попытке path traversal выбрасывает PathTraversalError.
        """
        music_path = self.music_sandbox.resolve(rel_mp3_path, allow_root=False)
        if music_path.exists():
            return music_path

        if self.tmp_sandbox is not None:
            try:
                tmp_path = self.tmp_sandbox.resolve(rel_mp3_path, allow_root=False)
                if tmp_path.exists():
                    return tmp_path
            except PathTraversalError:
                pass

            # Поддержка путей, начинающихся с имени корня tmp_sandbox (например, "tmp_downloads/...")
            normalized = rel_mp3_path.replace("\\", "/").lstrip("/")
            root_name = self.tmp_sandbox.root.name
            if normalized.startswith(f"{root_name}/"):
                stripped = normalized[len(root_name) + 1 :]
                try:
                    tmp_path = self.tmp_sandbox.resolve(stripped, allow_root=False)
                    if tmp_path.exists():
                        return tmp_path
                except PathTraversalError:
                    pass

        return music_path

    def get_lyrics_for_track(self, rel_mp3_path: str) -> LyricsResponse:
        """Считывает текст песни, расположенный рядом с целевым MP3 файлом.

        Приоритет отдается синхронизированному файлу .lrc; если он отсутствует,
        проверяется наличие текстового файла .txt.
        """
        mp3_path = self._resolve_track_path(rel_mp3_path)

        lrc_path = mp3_path.with_suffix(".lrc")
        if lrc_path.exists() and lrc_path.is_file():
            content = lrc_path.read_text(encoding="utf-8")
            return LyricsResponse(
                track_path=rel_mp3_path,
                lyrics=content,
                kind="lrc",
                synced=True,
            )

        txt_path = mp3_path.with_suffix(".txt")
        if txt_path.exists() and txt_path.is_file():
            content = txt_path.read_text(encoding="utf-8")
            return LyricsResponse(
                track_path=rel_mp3_path,
                lyrics=content,
                kind="txt",
                synced=False,
            )

        return LyricsResponse(
            track_path=rel_mp3_path,
            lyrics=None,
            kind="none",
            synced=False,
        )

    def save_lyrics(
        self,
        rel_mp3_path: str,
        content: str,
        kind: str = "auto",
    ) -> Path:
        """Сохраняет текст песни в файл рядом с целевым MP3-файлом.

        Если kind == 'auto', автоматически определяет расширение:
        наличие таймкодов вида [mm:ss -> .lrc, иначе -> .txt.
        Удаляет альтернативный файл (.txt при записи .lrc и наоборот) для исключения конфликтов.
        Никогда не вшивает текст в ID3-теги MP3-файла (инвариант Zero ID3 Lyrics).
        Возвращает абсолютный Path сохраненного файла текста.
        """
        mp3_path = self._resolve_track_path(rel_mp3_path)

        normalized_kind = kind.strip().lower() if kind else "auto"
        if normalized_kind == "auto":
            is_synced = bool(re.search(r"\[\d{2}:\d{2}", content))
            target_kind = "lrc" if is_synced else "txt"
        elif normalized_kind in ("lrc", "txt"):
            target_kind = normalized_kind
        else:
            is_synced = bool(re.search(r"\[\d{2}:\d{2}", content))
            target_kind = "lrc" if is_synced else "txt"

        target_file = mp3_path.with_suffix(f".{target_kind}")
        alt_file = mp3_path.with_suffix(".txt" if target_kind == "lrc" else ".lrc")

        target_file.parent.mkdir(parents=True, exist_ok=True)
        target_file.write_text(content, encoding="utf-8")

        if alt_file.exists() and alt_file.is_file():
            alt_file.unlink()

        return target_file

    def delete_lyrics(self, rel_mp3_path: str) -> bool:
        """Удаляет существующие файлы текстов (.lrc и .txt) рядом с MP3-файлом.

        Возвращает True, если хотя бы один файл был найден и удален, иначе False.
        """
        mp3_path = self._resolve_track_path(rel_mp3_path)
        deleted = False

        for ext in (".lrc", ".txt"):
            lyrics_file = mp3_path.with_suffix(ext)
            if lyrics_file.exists() and lyrics_file.is_file():
                lyrics_file.unlink()
                deleted = True

        return deleted
