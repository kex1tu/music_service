"""Асинхронный клиент для публичного REST API MusicBrainz (musicbrainz.org/ws/2/).

Обеспечивает поиск релизов, получение детальных метаданных, треклистов и обложек
через Cover Art Archive в строгом соответствии с требованиями MusicBrainz:
- Не более 1 запроса в секунду (Rate Limiting).
- Обязательный структурированный заголовок User-Agent для идентификации приложения.
- Обработка HTTP 429/503 с повтором после ожидания.
"""

import asyncio
import logging
import re
from typing import Any

import httpx

from app.core.exceptions import DownloadError, NotFoundError
from app.schemas.tag import CoverSearchResultItem

logger = logging.getLogger("music_manager.musicbrainz")

# Идентификация приложения в соответствии с MusicBrainz API guidelines:
# User-Agent: <AppName>/<Version> ( <ContactURL or Email> )
MUSICBRAINZ_USER_AGENT = "MusicManager/1.0.0 ( https://github.com/kex1tu/music_service )"
MUSICBRAINZ_BASE_URL = "https://musicbrainz.org/ws/2"
COVER_ART_ARCHIVE_BASE_URL = "https://coverartarchive.org"


class MusicBrainzClient:
    """Клиент для взаимодействия с MusicBrainz API и Cover Art Archive."""

    def __init__(
        self,
        http_client: httpx.AsyncClient | None = None,
        min_request_interval: float = 1.1,
    ) -> None:
        """Инициализирует клиент.

        Args:
            http_client: Опциональный переиспользуемый AsyncClient (для тестов и DI).
            min_request_interval: Минимальный интервал в секундах между запросами (по умолчанию 1.1 с).
        """
        self._external_client = http_client
        self._rate_lock = asyncio.Lock()
        self._last_request_time: float = 0.0
        self._min_interval = min_request_interval

    async def _get_client(self) -> httpx.AsyncClient:
        """Возвращает переданный или создает временный экземпляр AsyncClient."""
        if self._external_client is not None and not self._external_client.is_closed:
            return self._external_client
        return httpx.AsyncClient(
            timeout=httpx.Timeout(15.0, connect=10.0),
            headers={
                "User-Agent": MUSICBRAINZ_USER_AGENT,
                "Accept": "application/json",
            },
            follow_redirects=True,
        )

    async def _wait_for_rate_limit(self) -> None:
        """Обеспечивает соблюдение ограничения не более 1 запроса в секунду."""
        async with self._rate_lock:
            loop = asyncio.get_running_loop()
            now = loop.time()
            elapsed = now - self._last_request_time
            if elapsed < self._min_interval:
                wait_time = self._min_interval - elapsed
                logger.debug("MusicBrainz rate limiter: пауза %.2f с", wait_time)
                await asyncio.sleep(wait_time)
            self._last_request_time = loop.time()

    async def _request_api(
        self,
        endpoint: str,
        params: dict[str, Any] | None = None,
        max_retries: int = 2,
    ) -> dict[str, Any]:
        """Выполняет запрос к MusicBrainz API с соблюдением рейт-лимита и обработкой ошибок."""
        url = f"{MUSICBRAINZ_BASE_URL}/{endpoint.lstrip('/')}"
        merged_params = dict(params or {})
        merged_params["fmt"] = "json"

        client = await self._get_client()
        should_close = self._external_client is None

        try:
            for attempt in range(max_retries + 1):
                await self._wait_for_rate_limit()

                try:
                    response = await client.get(
                        url,
                        params=merged_params,
                        headers={
                            "User-Agent": MUSICBRAINZ_USER_AGENT,
                            "Accept": "application/json",
                        },
                    )
                except httpx.RequestError as exc:
                    if attempt == max_retries:
                        raise DownloadError(f"Ошибка сети при обращении к MusicBrainz: {exc}") from exc
                    await asyncio.sleep(1.5 * (attempt + 1))
                    continue

                if response.status_code == 404:
                    raise NotFoundError(f"Ресурс не найден в MusicBrainz: {endpoint}")

                if response.status_code in (429, 503):
                    if attempt < max_retries:
                        retry_after = 2.0
                        if "Retry-After" in response.headers:
                            try:
                                retry_after = float(response.headers["Retry-After"])
                            except ValueError:
                                retry_after = 2.0
                        logger.warning(
                            "MusicBrainz rate limit/503 (HTTP %s), повтор через %.1f с",
                            response.status_code,
                            retry_after,
                        )
                        await asyncio.sleep(max(retry_after, 1.2))
                        continue
                    raise DownloadError(
                        f"MusicBrainz API вернул статус ограничения частоты HTTP {response.status_code}"
                    )

                if response.status_code != 200:
                    raise DownloadError(
                        f"MusicBrainz API вернул HTTP {response.status_code}: {response.text[:200]}"
                    )

                try:
                    return response.json()
                except Exception as exc:
                    raise DownloadError(f"Некорректный JSON-ответ от MusicBrainz: {exc}") from exc

            raise DownloadError("Превышено количество попыток запроса к MusicBrainz")
        finally:
            if should_close:
                await client.aclose()

    @staticmethod
    def format_artist_credit(artist_credit: list[dict[str, Any]] | None) -> str:
        """Форматирует список артистов MusicBrainz с сохранением связующих фраз (feat., &, etc.)."""
        if not artist_credit or not isinstance(artist_credit, list):
            return "Unknown Artist"

        parts: list[str] = []
        for item in artist_credit:
            if isinstance(item, dict):
                name = item.get("name") or (item.get("artist", {}).get("name") if isinstance(item.get("artist"), dict) else "")
                joinphrase = item.get("joinphrase", "")
                if name:
                    parts.append(name)
                if joinphrase:
                    parts.append(joinphrase)
            elif isinstance(item, str):
                parts.append(item)

        return "".join(parts).strip() or "Unknown Artist"

    async def search_releases(
        self,
        query: str,
        limit: int = 10,
    ) -> list[dict[str, Any]]:
        """Ищет музыкальные релизы (альбомы/синглы/EP) по поисковому запросу.

        Args:
            query: Строка поиска (название альбома, артист или общий запрос).
            limit: Максимальное количество результатов.

        Returns:
            Список словарей с информацией о найденных релизах.
        """
        clean_q = query.strip()
        if not clean_q:
            return []

        # Формируем поисковый запрос
        lucene_query = f'release:"{clean_q}" OR artist:"{clean_q}" OR "{clean_q}"'

        data = await self._request_api(
            "/release/",
            params={"query": lucene_query, "limit": limit},
        )

        raw_releases = data.get("releases", [])
        results: list[dict[str, Any]] = []

        for r in raw_releases:
            if not isinstance(r, dict):
                continue
            mbid = r.get("id")
            if not mbid:
                continue

            title = r.get("title", "")
            artist_name = self.format_artist_credit(r.get("artist-credit"))

            # Год релиза (из поля 'date', например '1997-04-21' -> 1997)
            date_str = r.get("date") or ""
            year: int | None = None
            m = re.match(r"^(\d{4})", str(date_str))
            if m:
                year = int(m.group(1))

            track_count = r.get("track-count", 0)
            if not track_count and "media" in r:
                track_count = sum(m_item.get("track-count", 0) for m_item in r["media"] if isinstance(m_item, dict))

            cover_url = f"{COVER_ART_ARCHIVE_BASE_URL}/release/{mbid}/front-500"

            results.append({
                "id": mbid,
                "title": title,
                "artist": artist_name,
                "year": year,
                "nb_tracks": track_count,
                "country": r.get("country"),
                "status": r.get("status"),
                "cover_url": cover_url,
            })

        return results

    async def get_release(self, mbid: str) -> dict[str, Any]:
        """Получает полные метаданные релиза по MBID со всеми медиа-носителями и дорожками.

        Args:
            mbid: Идентификатор релиза в MusicBrainz.

        Returns:
            Словарь с метаданными релиза, списком дорожек и обложкой.
        """
        data = await self._request_api(
            f"/release/{mbid}",
            params={"inc": "recordings+artist-credits+genres+labels+release-groups"},
        )

        title = data.get("title", "")
        artist_name = self.format_artist_credit(data.get("artist-credit"))

        # Дата и год
        date_str = data.get("date") or ""
        year: int | None = None
        m = re.match(r"^(\d{4})", str(date_str))
        if m:
            year = int(m.group(1))

        # Жанры / теги
        genre_list: list[str] = []
        for g in data.get("genres", []):
            if isinstance(g, dict) and g.get("name"):
                genre_list.append(g["name"].title())
        if not genre_list:
            # Fallback на tags
            for t in data.get("tags", []):
                if isinstance(t, dict) and t.get("name"):
                    genre_list.append(t["name"].title())

        genre_str = "\\\\".join(genre_list[:3]) if genre_list else None

        # Разбор треклиста по всем дискам (media)
        tracks_items: list[dict[str, Any]] = []
        media_list = data.get("media", [])

        overall_track_pos = 1
        for m_idx, medium in enumerate(media_list, start=1):
            disc_num = medium.get("position") or m_idx
            medium_tracks = medium.get("tracks", [])

            for t_idx, track in enumerate(medium_tracks, start=1):
                # Порядковый номер
                t_pos = track.get("position") or t_idx
                t_title = track.get("title") or track.get("recording", {}).get("title", "")

                # Артист дорожки (если отличается от общего артиста релиза)
                track_artist_credit = track.get("artist-credit")
                if track_artist_credit:
                    t_artist = self.format_artist_credit(track_artist_credit)
                else:
                    rec_artist = track.get("recording", {}).get("artist-credit")
                    t_artist = self.format_artist_credit(rec_artist) if rec_artist else artist_name

                # Длительность в миллисекундах -> секундах
                length_ms = track.get("length") or track.get("recording", {}).get("length")
                duration_sec = round(length_ms / 1000) if length_ms else None

                tracks_items.append({
                    "track_number": int(t_pos) if len(media_list) == 1 else overall_track_pos,
                    "title": t_title,
                    "artist": t_artist,
                    "duration": duration_sec,
                    "disc_number": int(disc_num),
                })
                overall_track_pos += 1

        cover_url = f"{COVER_ART_ARCHIVE_BASE_URL}/release/{mbid}/front-500"

        return {
            "id": mbid,
            "title": title,
            "artist": artist_name,
            "year": year,
            "genre": genre_str,
            "cover_url": cover_url,
            "tracks": tracks_items,
        }

    async def search_covers(
        self,
        query: str,
        limit: int = 6,
    ) -> list[CoverSearchResultItem]:
        """Ищет обложки релизов высокого разрешения в MusicBrainz / Cover Art Archive."""
        releases = await self.search_releases(query, limit=limit * 2)
        items: list[CoverSearchResultItem] = []

        for r in releases:
            cover_url = r.get("cover_url")
            if not cover_url:
                continue

            items.append(
                CoverSearchResultItem(
                    source="musicbrainz",
                    title=r.get("title", ""),
                    artist=r.get("artist", ""),
                    cover_url=cover_url,
                    width=500,
                    height=500,
                    year=r.get("year"),
                )
            )
            if len(items) >= limit:
                break

        return items
