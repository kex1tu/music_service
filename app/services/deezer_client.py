"""Асинхронный клиент для публичного REST API Deezer (api.deezer.com).

Извлекает студийные метаданные треков, альбомов и плейлистов, включая
обложки максимального разрешения (1400x1400 cover_xl), номера дорожек, ISRC
и форматирование списка исполнителей. Не требует учетной записи или ARL.
"""

import logging
import re
from typing import Any, Literal

import httpx

from app.core.exceptions import DownloadError, NotFoundError

logger = logging.getLogger("music_manager.deezer")

DEEZER_URL_PATTERN = re.compile(
    r"(?:https?://)?(?:www\.)?deezer\.com/(?:[a-zA-Z]{2}(?:-[a-zA-Z]{2})?/)?(track|album|playlist|artist)/(\d+)",
    re.IGNORECASE,
)
DEEZER_SHORT_LINK_PATTERN = re.compile(
    r"(?:https?://)?(?:www\.)?(?:deezer\.page\.link/[a-zA-Z0-9_\-]+|link\.deezer\.com/(?:s/)?[a-zA-Z0-9_\-]+)",
    re.IGNORECASE,
)


class DeezerClient:
    """Клиент для взаимодействия с публичным API сервиса Deezer."""

    BASE_API_URL = "https://api.deezer.com"

    def __init__(self, http_client: httpx.AsyncClient | None = None) -> None:
        """Инициализирует клиент.

        Args:
            http_client: Опциональный переиспользуемый AsyncClient (для тестов и DI).
        """
        self._external_client = http_client

    async def _get_client(self) -> httpx.AsyncClient:
        """Возвращает переданный или создает временный экземпляр AsyncClient."""
        if self._external_client is not None and not self._external_client.is_closed:
            return self._external_client
        return httpx.AsyncClient(
            timeout=httpx.Timeout(15.0, connect=10.0),
            headers={"User-Agent": "MusicManager/1.0 (+https://github.com)"},
            follow_redirects=True,
        )

    async def parse_url(self, url: str) -> tuple[Literal["track", "album", "playlist", "artist"], str]:
        """Определяет тип ресурса и идентификатор из URL Deezer.

        Поддерживает:
        - Стандартные ссылки: https://www.deezer.com/album/302127
        - Локализованные ссылки: https://www.deezer.com/ru/track/3135556
        - Короткие ссылки с редиректом: https://deezer.page.link/XXXXX
        - Мобильные короткие ссылки: https://link.deezer.com/s/XXXXX

        Raises:
            DownloadError: Если формат URL некорректен или не распознан.
        """
        clean_url = url.strip()
        if not re.match(r"^https?://", clean_url, re.IGNORECASE):
            clean_url = f"https://{clean_url}"

        # Проверка коротких ссылок deezer.page.link и link.deezer.com (HEAD с fallback на GET)
        if DEEZER_SHORT_LINK_PATTERN.match(clean_url):
            try:
                client = await self._get_client()
                should_close = self._external_client is None
                try:
                    resolved_url: str | None = None
                    try:
                        response = await client.head(clean_url, follow_redirects=True)
                        candidate_url = str(response.url)
                        if DEEZER_URL_PATTERN.search(candidate_url):
                            resolved_url = candidate_url
                    except Exception as exc:  # noqa: BLE001
                        logger.debug("HEAD-запрос к '%s' не удался (%s), переходим к GET", clean_url, exc)

                    if not resolved_url:
                        response = await client.get(clean_url, follow_redirects=True)
                        resolved_url = str(response.url)

                    clean_url = resolved_url
                finally:
                    if should_close:
                        await client.aclose()
            except Exception as exc:
                raise DownloadError(f"Не удалось разрешить короткую ссылку Deezer '{url}': {exc}") from exc

        match = DEEZER_URL_PATTERN.search(clean_url)
        if not match:
            raise DownloadError(f"Строка '{url}' не является распознаваемым URL Deezer")

        resource_type = match.group(1).lower()
        resource_id = match.group(2)
        return resource_type, resource_id  # type: ignore[return-value]

    async def _request_api(self, endpoint: str, params: dict[str, Any] | None = None) -> dict[str, Any]:
        """Выполняет защищенный GET-запрос к API Deezer с разбором ошибок."""
        url = f"{self.BASE_API_URL}/{endpoint.lstrip('/')}"
        client = await self._get_client()
        should_close = self._external_client is None

        try:
            response = await client.get(url, params=params)
            if response.status_code == 404:
                raise NotFoundError(f"Ресурс Deezer не найден: {endpoint}")
            if response.status_code != 200:
                raise DownloadError(
                    f"Deezer API вернул HTTP {response.status_code} для {endpoint}"
                )

            data = response.json()
            if "error" in data:
                err_info = data["error"]
                err_msg = err_info.get("message", "Неизвестная ошибка API")
                err_code = err_info.get("code")
                if err_code == 800:  # Data not found in Deezer API
                    raise NotFoundError(f"Объект Deezer не найден: {err_msg}")
                raise DownloadError(f"Deezer API ошибка [{err_code}]: {err_msg}")

            return data
        except (NotFoundError, DownloadError):
            raise
        except Exception as exc:
            raise DownloadError(f"Сбой сетевого запроса к Deezer API ({endpoint}): {exc}") from exc
        finally:
            if should_close:
                await client.aclose()

    async def get_track(self, track_id: str) -> dict[str, Any]:
        """Запрашивает метаданные трека по ID."""
        return await self._request_api(f"/track/{track_id}")

    async def get_album(self, album_id: str, fetch_track_details: bool = False) -> dict[str, Any]:
        """Запрашивает метаданные альбома со всеми треками (с пагинацией и дозагрузкой деталей треков при необходимости)."""
        data = await self._request_api(f"/album/{album_id}")

        # Проверяем, есть ли дополнительные страницы треков
        tracks_obj = data.get("tracks", {})
        tracks_list = tracks_obj.get("data", [])
        next_url = tracks_obj.get("next")

        client = await self._get_client()
        should_close = self._external_client is None
        try:
            while next_url:
                response = await client.get(next_url)
                if response.status_code != 200:
                    break
                page_data = response.json()
                page_tracks = page_data.get("data", [])
                if not page_tracks:
                    break
                tracks_list.extend(page_tracks)
                next_url = page_data.get("next")

            if fetch_track_details and tracks_list:
                import asyncio
                sem = asyncio.Semaphore(10)

                async def _fetch_track_detail(t: dict[str, Any]) -> dict[str, Any]:
                    tid = t.get("id")
                    if not tid:
                        return t
                    async with sem:
                        try:
                            res = await client.get(f"{self.BASE_API_URL}/track/{tid}")
                            if res.status_code == 200:
                                t_detail = res.json()
                                if isinstance(t_detail, dict) and "error" not in t_detail:
                                    for k in ("track_position", "disk_number"):
                                        if k in t and (k not in t_detail or not t_detail[k]):
                                            t_detail[k] = t[k]
                                    return t_detail
                        except Exception as exc:  # noqa: BLE001
                            logger.debug("Не удалось получить детальные данные трека %s: %s", tid, exc)
                    return t

                enriched_tracks = await asyncio.gather(
                    *(_fetch_track_detail(t) for t in tracks_list),
                    return_exceptions=False,
                )
                tracks_list = list(enriched_tracks)
        finally:
            if should_close:
                await client.aclose()

        tracks_obj["data"] = tracks_list
        data["tracks"] = tracks_obj
        return data

    async def get_playlist(self, playlist_id: str, fetch_track_details: bool = False) -> dict[str, Any]:
        """Запрашивает метаданные плейлиста со всеми треками."""
        data = await self._request_api(f"/playlist/{playlist_id}")

        tracks_obj = data.get("tracks", {})
        tracks_list = tracks_obj.get("data", [])
        next_url = tracks_obj.get("next")

        client = await self._get_client()
        should_close = self._external_client is None
        try:
            while next_url and len(tracks_list) < 500:
                response = await client.get(next_url)
                if response.status_code != 200:
                    break
                page_data = response.json()
                page_tracks = page_data.get("data", [])
                if not page_tracks:
                    break
                tracks_list.extend(page_tracks)
                next_url = page_data.get("next")

            if fetch_track_details and tracks_list:
                import asyncio
                sem = asyncio.Semaphore(10)

                async def _fetch_track_detail(t: dict[str, Any]) -> dict[str, Any]:
                    tid = t.get("id")
                    if not tid:
                        return t
                    async with sem:
                        try:
                            res = await client.get(f"{self.BASE_API_URL}/track/{tid}")
                            if res.status_code == 200:
                                t_detail = res.json()
                                if isinstance(t_detail, dict) and "error" not in t_detail:
                                    for k in ("track_position", "disk_number"):
                                        if k in t and (k not in t_detail or not t_detail[k]):
                                            t_detail[k] = t[k]
                                    return t_detail
                        except Exception as exc:  # noqa: BLE001
                            logger.debug("Не удалось получить детальные данные трека %s: %s", tid, exc)
                    return t

                enriched_tracks = await asyncio.gather(
                    *(_fetch_track_detail(t) for t in tracks_list),
                    return_exceptions=False,
                )
                tracks_list = list(enriched_tracks)
        finally:
            if should_close:
                await client.aclose()

        tracks_obj["data"] = tracks_list
        data["tracks"] = tracks_obj
        return data

    async def search_tracks(self, query: str, limit: int = 5) -> list[dict[str, Any]]:
        """Выполняет поиск треков по текстовому запросу."""
        data = await self._request_api("/search/track", params={"q": query, "limit": limit})
        return data.get("data", [])

    async def search_albums(self, query: str, limit: int = 5) -> list[dict[str, Any]]:
        """Выполняет поиск альбомов по текстовому запросу."""
        data = await self._request_api("/search/album", params={"q": query, "limit": limit})
        return data.get("data", [])

    async def get_artist(self, artist_id: str) -> dict[str, Any]:
        """Получает метаданные исполнителя из Deezer API."""
        return await self._request_api(f"/artist/{artist_id}")

    async def get_artist_albums(self, artist_id: str, limit: int = 150) -> list[dict[str, Any]]:
        """Получает список релизов (альбомов, синглов, сборников) исполнителя."""
        data = await self._request_api(f"/artist/{artist_id}/albums", params={"limit": limit})
        return data.get("data", [])

    async def download_cover_bytes(self, cover_url: str) -> bytes:
        """Загружает бинарные данные обложки альбома."""
        client = await self._get_client()
        should_close = self._external_client is None
        try:
            resp = await client.get(cover_url)
            if resp.status_code != 200:
                raise DownloadError(f"Не удалось скачать обложку с {cover_url} (HTTP {resp.status_code})")
            return resp.content
        except Exception as exc:
            raise DownloadError(f"Сбой загрузки обложки: {exc}") from exc
        finally:
            if should_close:
                await client.aclose()

    @staticmethod
    def extract_best_cover_url(data: dict[str, Any]) -> str | None:
        """Извлекает URL обложки наивысшего качества (1400x1400 cover_xl)."""
        # Сначала проверяем альбом (если передан объект альбома или трек с вложенным album)
        album_obj = data.get("album", data)
        return (
            album_obj.get("cover_xl")
            or album_obj.get("cover_big")
            or album_obj.get("cover_medium")
            or album_obj.get("cover")
            or data.get("picture_xl")
            or data.get("picture_big")
        )

    KNOWN_COMPOUND_ARTISTS: tuple[str, ...] = (
        "Tyler, The Creator",
        "Tyler, the Creator",
        "Earth, Wind & Fire",
        "Earth, Wind and Fire",
        "Crosby, Stills, Nash & Young",
        "Crosby, Stills, Nash and Young",
        "Crosby, Stills & Nash",
        "Blood, Sweat & Tears",
        "Blood, Sweat and Tears",
        "Emerson, Lake & Palmer",
        "Peter, Paul and Mary",
        "Peter, Paul & Mary",
        "Bell, Book & Candle",
        "You, Me, and Everyone We Know",
        "Me First and the Gimme Gimmes",
        "Me, First and the Gimme Gimmes",
    )

    @staticmethod
    def split_artist_string(raw: str | None) -> list[str]:
        """Разбивает строку исполнителей по запятым, фитам и другим разделителям.

        Сохраняет целостность известных составных имен (например, Tyler, The Creator).
        """
        if not raw:
            return []
        text = str(raw).strip()
        if not text:
            return []

        # 1. Защита известных имен с запятыми/разделителями через плейсхолдеры
        placeholders: dict[str, str] = {}
        for idx, compound in enumerate(DeezerClient.KNOWN_COMPOUND_ARTISTS):
            if compound.lower() in text.lower():
                # Находим точное вхождение с сохранением регистра
                pattern = re.compile(re.escape(compound), re.IGNORECASE)
                m = pattern.search(text)
                if m:
                    token = f"__COMPOUND_ARTIST_{idx}__"
                    placeholders[token] = m.group(0)
                    text = pattern.sub(token, text)

        # 2. Регулярное выражение для разделителей исполнителей:
        # - \x00, \\+
        # - ; (с пробелами или без)
        # - , (запятая)
        # - & (с пробелами)
        # - / (с пробелами)
        # - feat., ft., featuring, with, pres., presents (с границами слов)
        # - vs., vs, v. (с границами слов)
        # - x, X, × (с окружающими пробелами)
        # - and, и (с границами слов и пробелами)
        split_pattern = re.compile(
            r"\x00|"
            r"\\+|"
            r"\s*;\s*|"
            r"\s*,\s*|"
            r"\s+&\s+|"
            r"\s+/\s+|"
            r"\s+(?:feat\.?|ft\.?|featuring|with|pres\.?|presents)\s+|"
            r"\s+(?:vs\.?|v\.)\s+|"
            r"\s+[xX×]\s+|"
            r"\s+(?:and|и)\s+",
            re.IGNORECASE,
        )

        tokens = split_pattern.split(text)
        results: list[str] = []

        for token in tokens:
            t = token.strip()
            if not t:
                continue
            # Восстанавливаем защищенные имена
            for p_key, p_val in placeholders.items():
                if p_key in t:
                    t = t.replace(p_key, p_val)
            t_clean = t.strip(" .")
            if t_clean and t_clean.lower() not in [r.lower() for r in results]:
                results.append(t_clean)

        return results

    @staticmethod
    def extract_feats_from_title(title: str | None, title_version: str | None = None) -> list[str]:
        """Извлекает имена приглашенных артистов (feat/ft/with) из полей title и title_version."""
        results: list[str] = []
        candidates: list[str] = []
        if title:
            candidates.append(str(title))
        if title_version:
            candidates.append(str(title_version))

        bracket_pattern = re.compile(
            r"[\(\[]\s*(?:feat\.?|ft\.?|featuring|with)\s+([^\]\)]+?)[\)\]]",
            re.IGNORECASE,
        )
        plain_pattern = re.compile(
            r"\b(?:feat\.?|ft\.?|featuring|with)\s+([^\[\]\(\)\-]+)$",
            re.IGNORECASE,
        )

        for text in candidates:
            for m in bracket_pattern.finditer(text):
                feat_raw = m.group(1).strip()
                for art in DeezerClient.split_artist_string(feat_raw):
                    if art and art.lower() not in [r.lower() for r in results]:
                        results.append(art)

            m_plain = plain_pattern.search(text)
            if m_plain:
                feat_raw = m_plain.group(1).strip()
                for art in DeezerClient.split_artist_string(feat_raw):
                    if art and art.lower() not in [r.lower() for r in results]:
                        results.append(art)

        return results

    @staticmethod
    def format_artists(track_data: Any) -> str:
        """Форматирует строку исполнителей с разделителем \\\\ для нескольких артистов.

        Поддерживает разбор contributors, artists, artist, фитов в названии и
        разделение по запятым / союзам.
        """
        if not track_data:
            return "Unknown Artist"

        if isinstance(track_data, str):
            names = DeezerClient.split_artist_string(track_data)
            return "\\\\".join(names) if names else (track_data.strip() or "Unknown Artist")

        if not isinstance(track_data, dict):
            return "Unknown Artist"

        collected_names: list[str] = []

        def add_name(n: str | None) -> None:
            if not n:
                return
            for part in DeezerClient.split_artist_string(str(n)):
                p_clean = part.strip()
                if p_clean and p_clean.lower() not in [x.lower() for x in collected_names]:
                    collected_names.append(p_clean)

        # 1. Проверяем contributors
        contributors = track_data.get("contributors", [])
        if isinstance(contributors, list) and contributors:
            perf_contribs = [
                c
                for c in contributors
                if isinstance(c, dict)
                and (
                    not c.get("role")
                    or str(c.get("role")).lower() in ("main", "featured", "primary", "performer")
                )
            ]
            target_contribs = perf_contribs if perf_contribs else contributors
            for c in target_contribs:
                if isinstance(c, dict):
                    add_name(c.get("name"))
                elif isinstance(c, str):
                    add_name(c)

        # 2. Проверяем список artists
        artists_list = track_data.get("artists", [])
        if isinstance(artists_list, list):
            for a in artists_list:
                if isinstance(a, dict):
                    add_name(a.get("name"))
                elif isinstance(a, str):
                    add_name(a)

        # 3. Проверяем одиночный объект artist
        artist_obj = track_data.get("artist")
        if isinstance(artist_obj, dict):
            add_name(artist_obj.get("name"))
        elif isinstance(artist_obj, str):
            add_name(artist_obj)

        # 4. Извлекаем фиты из title / title_version
        title = track_data.get("title")
        title_ver = track_data.get("title_version")
        if title or title_ver:
            for feat in DeezerClient.extract_feats_from_title(title, title_ver):
                add_name(feat)

        if collected_names:
            return "\\\\".join(collected_names)

        return "Unknown Artist"
