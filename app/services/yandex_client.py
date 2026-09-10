"""Асинхронный клиент для взаимодействия с Яндекс Музыкой.

Извлекает студийные метаданные треков, альбомов и плейлистов без авторизации
через публичные JSON-хэндлеры (handlers/*.jsx), форматирует обложки высокого
разрешения (1000x1000) и поддерживает прямое скачивание с серверов Яндекса
при наличии OAuth токена Яндекс Плюса.
"""

import hashlib
import logging
import re
import xml.etree.ElementTree as ET
from typing import Any, Literal

import httpx

from app.core.exceptions import DownloadError, NotFoundError

logger = logging.getLogger("music_manager.yandex")

YANDEX_MUSIC_DOMAINS = (
    "music.yandex.ru",
    "music.yandex.com",
    "music.yandex.by",
    "music.yandex.kz",
)

# Паттерны ссылок Яндекс Музыки
TRACK_WITH_ALBUM_PATTERN = re.compile(
    r"(?:https?://)?(?:www\.)?music\.yandex\.(?:ru|com|by|kz)/album/(\d+)/track/(\d+)",
    re.IGNORECASE,
)
TRACK_SHORT_PATTERN = re.compile(
    r"(?:https?://)?(?:www\.)?music\.yandex\.(?:ru|com|by|kz)/track/(\d+)",
    re.IGNORECASE,
)
ALBUM_PATTERN = re.compile(
    r"(?:https?://)?(?:www\.)?music\.yandex\.(?:ru|com|by|kz)/album/(\d+)/?$",
    re.IGNORECASE,
)
PLAYLIST_USER_PATTERN = re.compile(
    r"(?:https?://)?(?:www\.)?music\.yandex\.(?:ru|com|by|kz)/users/([^/]+)/playlists/(\d+)",
    re.IGNORECASE,
)
PLAYLIST_UUID_PATTERN = re.compile(
    r"(?:https?://)?(?:www\.)?music\.yandex\.(?:ru|com|by|kz)/playlist/([a-zA-Z0-9_\-]+)",
    re.IGNORECASE,
)


class YandexMusicClient:
    """Клиент для извлечения метаданных и аудиопотоков Яндекс Музыки."""

    BASE_WEB_URL = "https://music.yandex.ru"
    BASE_API_URL = "https://api.music.yandex.net"

    def __init__(
        self,
        token: str | None = None,
        http_client: httpx.AsyncClient | None = None,
    ) -> None:
        self._token = token.strip() if token else None
        self._external_client = http_client

    async def _get_client(self) -> httpx.AsyncClient:
        if self._external_client is not None and not self._external_client.is_closed:
            return self._external_client
        headers = {
            "User-Agent": (
                "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
                "(KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
            ),
            "Accept": "application/json",
        }
        if self._token:
            headers["Authorization"] = f"OAuth {self._token}"
        return httpx.AsyncClient(
            timeout=httpx.Timeout(15.0, connect=10.0),
            headers=headers,
            follow_redirects=True,
        )

    async def parse_url(self, url: str) -> tuple[Literal["track", "album", "playlist"], dict[str, str]]:
        """Определяет тип ресурса и параметры из URL Яндекс Музыки.

        Raises:
            DownloadError: Если формат URL некорректен или не распознан.
        """
        clean_url = url.strip()
        if not re.match(r"^https?://", clean_url, re.IGNORECASE):
            clean_url = f"https://{clean_url}"

        # 1. Трек в альбоме: /album/123/track/456
        m = TRACK_WITH_ALBUM_PATTERN.search(clean_url)
        if m:
            return "track", {"album_id": m.group(1), "track_id": m.group(2)}

        # 2. Прямой трек: /track/456
        m = TRACK_SHORT_PATTERN.search(clean_url)
        if m:
            return "track", {"track_id": m.group(1)}

        # 3. Альбом: /album/123
        m = ALBUM_PATTERN.search(clean_url)
        if m:
            return "album", {"album_id": m.group(1)}

        # 4. Плейлист пользователя: /users/{owner}/playlists/{kind}
        m = PLAYLIST_USER_PATTERN.search(clean_url)
        if m:
            return "playlist", {"owner": m.group(1), "kind": m.group(2)}

        # 5. Ссылка с UUID плейлиста: /playlist/{uuid} (разрешаем редирект)
        m = PLAYLIST_UUID_PATTERN.search(clean_url)
        if m:
            client = await self._get_client()
            should_close = self._external_client is None
            try:
                resp = await client.get(clean_url, follow_redirects=True)
                final_url = str(resp.url)
                m_user = PLAYLIST_USER_PATTERN.search(final_url)
                if m_user:
                    return "playlist", {"owner": m_user.group(1), "kind": m_user.group(2)}
                # Если в URL не появилось /users/, возвращаем uuid
                return "playlist", {"uuid": m.group(1)}
            except Exception as exc:  # noqa: BLE001
                logger.warning("Не удалось разрешить плейлист по ссылке '%s': %s", clean_url, exc)
                return "playlist", {"uuid": m.group(1)}
            finally:
                if should_close:
                    await client.aclose()

        raise DownloadError(f"Строка '{url}' не является распознаваемым URL Яндекс Музыки")

    @staticmethod
    def format_cover(cover_uri: str | None, size: str = "1000x1000") -> str | None:
        """Форматирует URL обложки Яндекс Музыки с высоким разрешением."""
        if not cover_uri:
            return None
        formatted = cover_uri.replace("%%", size)
        if not formatted.startswith("http"):
            formatted = f"https://{formatted.lstrip('/')}"
        return formatted

    @staticmethod
    def format_artists(artists_data: list[dict[str, Any]] | None) -> str:
        """Форматирует имена исполнителей через разделитель \\\\."""
        if not artists_data:
            return "Unknown Artist"
        names: list[str] = []
        for a in artists_data:
            if isinstance(a, dict) and a.get("name"):
                names.append(a["name"].strip())
            elif isinstance(a, str) and a.strip():
                names.append(a.strip())
        return "\\\\".join(names) if names else "Unknown Artist"

    async def get_track(self, track_id: str) -> dict[str, Any]:
        """Запрашивает метаданные одиночного трека."""
        client = await self._get_client()
        should_close = self._external_client is None
        try:
            url = f"{self.BASE_WEB_URL}/handlers/track.jsx?track={track_id}"
            resp = await client.get(url)
            if resp.status_code == 404:
                raise NotFoundError(f"Трек Яндекс Музыки {track_id} не найден")
            if resp.status_code != 200:
                raise DownloadError(f"Яндекс Музыка вернула HTTP {resp.status_code} для трека {track_id}")
            data = resp.json()
            track = data.get("track")
            if not track:
                raise NotFoundError(f"Метаданные трека {track_id} отсутствуют в ответе")
            return track
        except (NotFoundError, DownloadError):
            raise
        except Exception as exc:
            raise DownloadError(f"Ошибка запроса трека {track_id}: {exc}") from exc
        finally:
            if should_close:
                await client.aclose()

    async def get_album(self, album_id: str) -> dict[str, Any]:
        """Запрашивает информацию об альбоме и список треков."""
        client = await self._get_client()
        should_close = self._external_client is None
        try:
            url = f"{self.BASE_WEB_URL}/handlers/album.jsx?album={album_id}"
            resp = await client.get(url)
            if resp.status_code == 404:
                raise NotFoundError(f"Альбом Яндекс Музыки {album_id} не найден")
            if resp.status_code != 200:
                raise DownloadError(f"Яндекс Музыка вернула HTTP {resp.status_code} для альбома {album_id}")
            data = resp.json()
            if not data or not data.get("title"):
                raise NotFoundError(f"Альбом {album_id} не содержит метаданных")
            return data
        except (NotFoundError, DownloadError):
            raise
        except Exception as exc:
            raise DownloadError(f"Ошибка запроса альбома {album_id}: {exc}") from exc
        finally:
            if should_close:
                await client.aclose()

    async def get_playlist(self, owner: str, kind: str) -> dict[str, Any]:
        """Запрашивает информацию о плейлисте и входящих в него треках."""
        client = await self._get_client()
        should_close = self._external_client is None
        try:
            url = f"{self.BASE_WEB_URL}/handlers/playlist.jsx?owner={owner}&kinds={kind}"
            resp = await client.get(url)
            if resp.status_code == 404:
                raise NotFoundError(f"Плейлист Яндекс Музыки {owner}/{kind} не найден")
            if resp.status_code != 200:
                raise DownloadError(f"Яндекс Музыка вернула HTTP {resp.status_code} для плейлиста {owner}/{kind}")
            data = resp.json()
            playlist = data.get("playlist")
            if not playlist:
                raise NotFoundError(f"Плейлист {owner}/{kind} недоступен или пуст")
            return playlist
        except (NotFoundError, DownloadError):
            raise
        except Exception as exc:
            raise DownloadError(f"Ошибка запроса плейлиста {owner}/{kind}: {exc}") from exc
        finally:
            if should_close:
                await client.aclose()

    async def get_direct_download_url(self, track_id: str) -> str | None:
        """Получает прямую ссылку на скачивание MP3 файла с CDN Яндекса при наличии токена."""
        if not self._token:
            return None

        client = await self._get_client()
        should_close = self._external_client is None
        try:
            # 1. Запрашиваем информацию о загрузке трека
            info_url = f"{self.BASE_API_URL}/tracks/{track_id}/download-info"
            resp = await client.get(info_url)
            if resp.status_code != 200:
                logger.debug("Запрос download-info для трека %s вернул HTTP %s", track_id, resp.status_code)
                return None

            result = resp.json().get("result", [])
            if not result:
                return None

            # Выбираем MP3 наивысшего качества (битрейт 320 или наибольший)
            mp3_sources = [s for s in result if s.get("codec") == "mp3"]
            if not mp3_sources:
                mp3_sources = result
            best_src = max(mp3_sources, key=lambda s: int(s.get("bitrateInKbps") or 0))
            download_info_url = best_src.get("downloadInfoUrl")
            if not download_info_url:
                return None

            # 2. Получаем XML с хостом и солью
            xml_resp = await client.get(download_info_url)
            if xml_resp.status_code != 200:
                return None

            root = ET.fromstring(xml_resp.text)
            host = root.findtext("host")
            path = root.findtext("path")
            ts = root.findtext("ts")
            s = root.findtext("s")

            if not (host and path and ts and s):
                return None

            # 3. Вычисляем хэш подписи: md5("XGRlBW9FXlekgbPrrGkpAnalytics" + path[1:] + s)
            sign_str = f"XGRlBW9FXlekgbPrrGkpAnalytics{path[1:]}{s}"
            sign_hash = hashlib.md5(sign_str.encode("utf-8")).hexdigest()

            return f"https://{host}/get-mp3/{sign_hash}/{ts}{path}"
        except Exception as exc:  # noqa: BLE001
            logger.warning("Не удалось сгенерировать прямую ссылку Яндекс Музыки для трека %s: %s", track_id, exc)
            return None
        finally:
            if should_close:
                await client.aclose()
