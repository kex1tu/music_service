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

    async def get_album(self, album_id: str) -> dict[str, Any]:
        """Запрашивает метаданные альбома со всеми треками (с пагинацией при необходимости)."""
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
        finally:
            if should_close:
                await client.aclose()

        tracks_obj["data"] = tracks_list
        data["tracks"] = tracks_obj
        return data

    async def get_playlist(self, playlist_id: str) -> dict[str, Any]:
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

    @staticmethod
    def format_artists(track_data: dict[str, Any]) -> str:
        """Форматирует строку исполнителей с разделителем \\\\ для нескольких артистов.

        Использует contributors или список artists, если они доступны.
        """
        contributors = track_data.get("contributors", [])
        if contributors:
            names: list[str] = []
            for c in contributors:
                name = c.get("name")
                if name and name not in names:
                    names.append(name)
            if names:
                return "\\\\".join(names)

        artist_obj = track_data.get("artist", {})
        main_name = artist_obj.get("name") if isinstance(artist_obj, dict) else None
        return main_name or "Unknown Artist"
