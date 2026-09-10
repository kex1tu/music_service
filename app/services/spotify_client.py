"""Асинхронный клиент для взаимодействия со Spotify.

Извлекает метаданные треков, альбомов и плейлистов без обязательных API-ключей
через разбор открытых страниц и Next.js Embed API, а при наличии учетных данных
(SPOTIFY_CLIENT_ID / SPOTIFY_CLIENT_SECRET) использует официальный Web API
с поддержкой постраничной загрузки больших плейлистов.
"""

import base64
import json
import logging
import re
import time
from typing import Any, Literal

import httpx

from app.core.exceptions import DownloadError, NotFoundError

logger = logging.getLogger("music_manager.spotify")

SPOTIFY_URL_PATTERN = re.compile(
    r"(?:https?://)?(?:open\.)?spotify\.com/(?:[a-zA-Z]{2}(?:-[a-zA-Z]{2})?/)?(track|album|playlist)/([a-zA-Z0-9]+)",
    re.IGNORECASE,
)
SPOTIFY_LINK_PATTERN = re.compile(
    r"(?:https?://)?(?:www\.)?spotify\.link/([a-zA-Z0-9]+)",
    re.IGNORECASE,
)


class SpotifyClient:
    """Клиент для извлечения метаданных из Spotify."""

    def __init__(
        self,
        client_id: str | None = None,
        client_secret: str | None = None,
        http_client: httpx.AsyncClient | None = None,
    ) -> None:
        self._client_id = client_id.strip() if client_id else None
        self._client_secret = client_secret.strip() if client_secret else None
        self._external_client = http_client
        self._access_token: str | None = None
        self._token_expires_at: float = 0.0

    async def _get_client(self) -> httpx.AsyncClient:
        if self._external_client is not None and not self._external_client.is_closed:
            return self._external_client
        return httpx.AsyncClient(
            timeout=httpx.Timeout(15.0, connect=10.0),
            headers={
                "User-Agent": (
                    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
                    "(KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
                ),
                "Accept-Language": "en-US,en;q=0.9",
            },
            follow_redirects=True,
        )

    async def parse_url(self, url: str) -> tuple[Literal["track", "album", "playlist"], str]:
        """Определяет тип ресурса и идентификатор Spotify.

        Raises:
            DownloadError: Если формат URL некорректен или не распознан.
        """
        clean_url = url.strip()
        if not re.match(r"^https?://", clean_url, re.IGNORECASE):
            clean_url = f"https://{clean_url}"

        # Короткие ссылки spotify.link
        if SPOTIFY_LINK_PATTERN.search(clean_url):
            client = await self._get_client()
            should_close = self._external_client is None
            try:
                resp = await client.get(clean_url, follow_redirects=True)
                clean_url = str(resp.url)
            except Exception as exc:
                raise DownloadError(f"Не удалось разрешить короткую ссылку Spotify '{url}': {exc}") from exc
            finally:
                if should_close:
                    await client.aclose()

        m = SPOTIFY_URL_PATTERN.search(clean_url)
        if not m:
            raise DownloadError(f"Строка '{url}' не является распознаваемым URL Spotify")

        res_type = m.group(1).lower()
        res_id = m.group(2)
        return res_type, res_id  # type: ignore[return-value]

    async def _get_api_token(self) -> str | None:
        """Получает или обновляет App Access Token через Client Credentials Flow."""
        if not (self._client_id and self._client_secret):
            return None

        now = time.time()
        if self._access_token and now < self._token_expires_at - 60:
            return self._access_token

        client = await self._get_client()
        should_close = self._external_client is None
        try:
            auth_header = base64.b64encode(f"{self._client_id}:{self._client_secret}".encode()).decode()
            resp = await client.post(
                "https://accounts.spotify.com/api/token",
                data={"grant_type": "client_credentials"},
                headers={"Authorization": f"Basic {auth_header}"},
            )
            if resp.status_code == 200:
                data = resp.json()
                self._access_token = data.get("access_token")
                expires_in = int(data.get("expires_in", 3600))
                self._token_expires_at = now + expires_in
                return self._access_token
            logger.warning("Не удалось получить токен Spotify Web API (HTTP %s): %s", resp.status_code, resp.text)
            return None
        except Exception as exc:  # noqa: BLE001
            logger.warning("Ошибка авторизации Spotify API: %s", exc)
            return None
        finally:
            if should_close:
                await client.aclose()

    async def _scrape_embed(self, res_type: str, res_id: str) -> dict[str, Any]:
        """Скрейпит публичные метаданные со страницы embed без API ключей."""
        embed_url = f"https://open.spotify.com/embed/{res_type}/{res_id}"
        client = await self._get_client()
        should_close = self._external_client is None
        try:
            resp = await client.get(embed_url)
            if resp.status_code == 404:
                raise NotFoundError(f"Ресурс Spotify {res_type}/{res_id} не найден")
            if resp.status_code != 200:
                raise DownloadError(f"Сбой загрузки embed страницы Spotify ({resp.status_code})")

            html = resp.text
            # Ищем скрипт Next.js данных
            m = re.search(r'<script\s+id="__NEXT_DATA__"\s+type="application/json">([^<]+)</script>', html)
            if not m:
                # Попробуем альтернативный regex
                m = re.search(r'<script\s+id="initial-state"\s+type="text/plain">([^<]+)</script>', html)
                if m:
                    raw_b64 = m.group(1).strip()
                    decoded = base64.b64decode(raw_b64).decode("utf-8")
                    return json.loads(decoded)
                raise DownloadError("Не удалось извлечь структурированные метаданные со страницы Spotify")

            data = json.loads(m.group(1))
            props = data.get("props", {}).get("pageProps", {})
            entity = (
                props.get("state", {}).get("data", {}).get("entity")
                or props.get("state", {}).get("data", {}).get(res_type)
                or props.get("entity", {})
            )
            if not entity:
                raise NotFoundError(f"Метаданные для {res_type}/{res_id} отсутствуют")
            return entity
        finally:
            if should_close:
                await client.aclose()

    @staticmethod
    def extract_cover_from_embed(entity: dict[str, Any]) -> str | None:
        """Извлекает обложку наивысшего разрешения из данных embed."""
        cover_art = entity.get("coverArt", {})
        sources = cover_art.get("sources", []) if isinstance(cover_art, dict) else []
        if sources:
            best = max(sources, key=lambda s: int(s.get("width") or 0))
            return best.get("url")
        images = entity.get("images", [])
        if images:
            return images[0].get("url")
        return None

    @staticmethod
    def format_artists(artists_data: Any) -> str:
        """Форматирует строку артистов через разделитель \\\\."""
        if not artists_data:
            return "Unknown Artist"
        if isinstance(artists_data, str):
            return artists_data.strip() or "Unknown Artist"
        names: list[str] = []
        if isinstance(artists_data, list):
            for a in artists_data:
                if isinstance(a, dict) and a.get("name"):
                    names.append(a["name"].strip())
                elif isinstance(a, str) and a.strip():
                    names.append(a.strip())
        return "\\\\".join(names) if names else "Unknown Artist"

    async def get_track(self, track_id: str) -> dict[str, Any]:
        """Получает метаданные трека."""
        token = await self._get_api_token()
        if token:
            client = await self._get_client()
            should_close = self._external_client is None
            try:
                resp = await client.get(
                    f"https://api.spotify.com/v1/tracks/{track_id}",
                    headers={"Authorization": f"Bearer {token}"},
                )
                if resp.status_code == 200:
                    data = resp.json()
                    album = data.get("album", {})
                    images = album.get("images", [])
                    return {
                        "id": track_id,
                        "title": data.get("name", "Unknown Track"),
                        "artist": self.format_artists(data.get("artists", [])),
                        "album": album.get("name", ""),
                        "album_artist": self.format_artists(album.get("artists", [])),
                        "year": int(album.get("release_date", "0")[:4]) if album.get("release_date") else None,
                        "duration": round((data.get("duration_ms") or 0) / 1000.0),
                        "cover_url": images[0].get("url") if images else None,
                        "isrc": data.get("external_ids", {}).get("isrc"),
                    }
            except Exception as exc:  # noqa: BLE001
                logger.debug("Spotify Web API get_track error (%s), fallback to embed", exc)
            finally:
                if should_close:
                    await client.aclose()

        entity = await self._scrape_embed("track", track_id)
        cover_url = self.extract_cover_from_embed(entity)
        artists = entity.get("artists", [])
        artists_str = "\\\\".join(a.get("name", "") for a in artists if a.get("name")) or entity.get("subtitle", "Unknown Artist")
        duration_ms = entity.get("duration") or 0
        return {
            "id": track_id,
            "title": entity.get("name") or entity.get("title", "Unknown Track"),
            "artist": artists_str,
            "album": entity.get("album", {}).get("name") if isinstance(entity.get("album"), dict) else "",
            "album_artist": artists_str,
            "year": None,
            "duration": round(duration_ms / 1000.0),
            "cover_url": cover_url,
            "isrc": entity.get("isrc"),
        }

    async def get_album(self, album_id: str) -> dict[str, Any]:
        """Получает метаданные альбома со списком треков."""
        token = await self._get_api_token()
        if token:
            client = await self._get_client()
            should_close = self._external_client is None
            try:
                resp = await client.get(
                    f"https://api.spotify.com/v1/albums/{album_id}",
                    headers={"Authorization": f"Bearer {token}"},
                )
                if resp.status_code == 200:
                    data = resp.json()
                    album_title = data.get("name", "Unknown Album")
                    album_artist = self.format_artists(data.get("artists", []))
                    year = int(data.get("release_date", "0")[:4]) if data.get("release_date") else None
                    images = data.get("images", [])
                    cover_url = images[0].get("url") if images else None

                    tracks: list[dict[str, Any]] = []
                    raw_tracks = data.get("tracks", {}).get("items", [])
                    for idx, t in enumerate(raw_tracks, start=1):
                        tracks.append({
                            "id": t.get("id", str(idx)),
                            "track_number": t.get("track_number", idx),
                            "title": t.get("name", "Unknown Track"),
                            "artist": self.format_artists(t.get("artists", [])),
                            "duration": round((t.get("duration_ms") or 0) / 1000.0),
                            "isrc": t.get("external_ids", {}).get("isrc"),
                        })
                    return {
                        "id": album_id,
                        "title": album_title,
                        "artist": album_artist,
                        "year": year,
                        "cover_url": cover_url,
                        "tracks": tracks,
                    }
            except Exception as exc:  # noqa: BLE001
                logger.debug("Spotify Web API get_album error (%s), fallback to embed", exc)
            finally:
                if should_close:
                    await client.aclose()

        entity = await self._scrape_embed("album", album_id)
        album_title = entity.get("name") or entity.get("title", "Unknown Album")
        album_artist = entity.get("subtitle") or self.format_artists(entity.get("artists", []))
        cover_url = self.extract_cover_from_embed(entity)

        tracks = []
        raw_tracks = entity.get("trackList") or entity.get("tracks", [])
        for idx, t in enumerate(raw_tracks, start=1):
            dur_ms = t.get("duration") or t.get("durationMs") or 0
            t_artist = t.get("subtitle") or self.format_artists(t.get("artists", [])) or album_artist
            tracks.append({
                "id": t.get("id") or t.get("uri") or str(idx),
                "track_number": idx,
                "title": t.get("title") or t.get("name", "Unknown Track"),
                "artist": t_artist,
                "duration": round(dur_ms / 1000.0),
                "isrc": t.get("isrc"),
            })

        return {
            "id": album_id,
            "title": album_title,
            "artist": album_artist,
            "year": None,
            "cover_url": cover_url,
            "tracks": tracks,
        }

    async def get_playlist(self, playlist_id: str) -> dict[str, Any]:
        """Получает метаданные плейлиста со списком треков."""
        token = await self._get_api_token()
        if token:
            client = await self._get_client()
            should_close = self._external_client is None
            try:
                resp = await client.get(
                    f"https://api.spotify.com/v1/playlists/{playlist_id}",
                    headers={"Authorization": f"Bearer {token}"},
                )
                if resp.status_code == 200:
                    data = resp.json()
                    pl_title = data.get("name", "Unknown Playlist")
                    pl_owner = data.get("owner", {}).get("display_name", "Spotify")
                    images = data.get("images", [])
                    cover_url = images[0].get("url") if images else None

                    tracks: list[dict[str, Any]] = []
                    raw_items = data.get("tracks", {}).get("items", [])
                    for idx, item in enumerate(raw_items, start=1):
                        t = item.get("track")
                        if not t or not isinstance(t, dict):
                            continue
                        album = t.get("album", {})
                        tracks.append({
                            "id": t.get("id", str(idx)),
                            "track_number": idx,
                            "title": t.get("name", "Unknown Track"),
                            "artist": self.format_artists(t.get("artists", [])),
                            "album": album.get("name", ""),
                            "album_artist": self.format_artists(album.get("artists", [])),
                            "year": int(album.get("release_date", "0")[:4]) if album.get("release_date") else None,
                            "duration": round((t.get("duration_ms") or 0) / 1000.0),
                            "isrc": t.get("external_ids", {}).get("isrc"),
                        })
                    return {
                        "id": playlist_id,
                        "title": pl_title,
                        "artist": pl_owner,
                        "cover_url": cover_url,
                        "tracks": tracks,
                    }
            except Exception as exc:  # noqa: BLE001
                logger.debug("Spotify Web API get_playlist error (%s), fallback to embed", exc)
            finally:
                if should_close:
                    await client.aclose()

        entity = await self._scrape_embed("playlist", playlist_id)
        pl_title = entity.get("name") or entity.get("title", "Unknown Playlist")
        pl_owner = entity.get("subtitle", "Spotify")
        cover_url = self.extract_cover_from_embed(entity)

        tracks = []
        raw_tracks = entity.get("trackList") or entity.get("tracks", [])
        for idx, t in enumerate(raw_tracks, start=1):
            dur_ms = t.get("duration") or t.get("durationMs") or 0
            t_artist = t.get("subtitle") or self.format_artists(t.get("artists", [])) or pl_owner
            tracks.append({
                "id": t.get("id") or t.get("uri") or str(idx),
                "track_number": idx,
                "title": t.get("title") or t.get("name", "Unknown Track"),
                "artist": t_artist,
                "album": pl_title,
                "album_artist": pl_owner,
                "year": None,
                "duration": round(dur_ms / 1000.0),
                "isrc": t.get("isrc"),
            })

        return {
            "id": playlist_id,
            "title": pl_title,
            "artist": pl_owner,
            "cover_url": cover_url,
            "tracks": tracks,
        }
