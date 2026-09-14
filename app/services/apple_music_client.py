"""Асинхронный клиент для взаимодействия с Apple Music.

Извлекает студийные метаданные треков, альбомов и плейлистов без токенов разработчика
через разбор открытых страниц Apple Music (serialized-server-data и JSON-LD),
форматирует обложки максимального разрешения (1400x1400) и готовит дорожки
для последующего сопоставления.
"""

import json
import logging
import re
from typing import Any, Literal

import httpx

from app.core.exceptions import DownloadError, NotFoundError

logger = logging.getLogger("music_manager.apple_music")

APPLE_MUSIC_URL_PATTERN = re.compile(
    r"(?:https?://)?(?:www\.)?music\.apple\.com/([a-zA-Z]{2})/([^/]+)/([^/]+)/([a-zA-Z0-9\._\-]+)",
    re.IGNORECASE,
)


class AppleMusicClient:
    """Клиент для извлечения метаданных из публичных страниц Apple Music."""

    def __init__(self, http_client: httpx.AsyncClient | None = None) -> None:
        self._external_client = http_client

    async def _get_client(self) -> httpx.AsyncClient:
        if self._external_client is not None and not self._external_client.is_closed:
            return self._external_client
        return httpx.AsyncClient(
            timeout=httpx.Timeout(15.0, connect=10.0),
            headers={
                "User-Agent": (
                    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 "
                    "(KHTML, like Gecko) Version/17.0 Safari/605.1.15"
                ),
                "Accept-Language": "en-US,en;q=0.9",
                "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
            },
            follow_redirects=True,
        )

    def parse_url(self, url: str) -> tuple[Literal["track", "album", "playlist", "artist"], dict[str, str]]:
        """Определяет тип ресурса (track, album, playlist, artist) и идентификаторы Apple Music.

        Raises:
            DownloadError: Если формат URL некорректен или не распознан.
        """
        clean_url = url.strip()
        if not re.match(r"^https?://", clean_url, re.IGNORECASE):
            clean_url = f"https://{clean_url}"

        # Проверяем query-параметр ?i=123 (одиночный трек внутри альбома)
        m_track_in_album = re.search(r"/album/(?:[^/]+/)?(\d+).*?[?&]i=(\d+)", clean_url, re.IGNORECASE)
        if m_track_in_album:
            return "track", {"album_id": m_track_in_album.group(1), "track_id": m_track_in_album.group(2)}

        # Одиночный трек /song/name/123
        m_song = re.search(r"/song/(?:[^/]+/)?(\d+)", clean_url, re.IGNORECASE)
        if m_song:
            return "track", {"track_id": m_song.group(1)}

        # Плейлист /playlist/name/pl.xxx или /playlist/pl.xxx
        m_pl = re.search(r"/playlist/(?:[^/]+/)?(pl\.[a-zA-Z0-9\._\-]+)", clean_url, re.IGNORECASE)
        if m_pl:
            return "playlist", {"playlist_id": m_pl.group(1)}

        # Альбом /album/name/123 или /album/123
        m_album = re.search(r"/album/(?:[^/]+/)?(\d+)", clean_url, re.IGNORECASE)
        if m_album:
            return "album", {"album_id": m_album.group(1)}

        # Артист /artist/name/123 или /artist/123
        m_art = re.search(r"/artist/(?:[^/]+/)?(\d+)", clean_url, re.IGNORECASE)
        if m_art:
            return "artist", {"artist_id": m_art.group(1)}

        raise DownloadError(f"Строка '{url}' не является распознаваемым URL Apple Music")

    @staticmethod
    def format_cover(artwork_url: str | None, width: int = 1400, height: int = 1400) -> str | None:
        """Форматирует URL обложки Apple Music с заменой шаблона {w}x{h}bb на реальный размер."""
        if not artwork_url:
            return None
        # Заменяем {w}x{h}bb или {w}x{h}
        res = artwork_url.replace("{w}x{h}", f"{width}x{height}")
        res = res.replace("{f}", "jpg")
        return res

    @staticmethod
    def parse_iso_duration(dur_str: str | None) -> int:
        """Парсит длительность в формате ISO 8601 (например, PT3M45S) в секунды."""
        if not dur_str:
            return 0
        m = re.match(r"^PT(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?$", dur_str)
        if not m:
            return 0
        h = int(m.group(1) or 0)
        minutes = int(m.group(2) or 0)
        s = int(m.group(3) or 0)
        return h * 3600 + minutes * 60 + s

    async def fetch_page_data(self, url: str) -> dict[str, Any]:
        """Загружает страницу Apple Music и извлекает структурированные данные."""
        client = await self._get_client()
        should_close = self._external_client is None
        try:
            resp = await client.get(url)
            if resp.status_code == 404:
                raise NotFoundError(f"Страница Apple Music не найдена: {url}")
            if resp.status_code != 200:
                raise DownloadError(f"Apple Music вернул HTTP {resp.status_code}")

            html = resp.text

            # 1. Попытка извлечь serialized-server-data
            m_server = re.search(
                r'<script\s+type="application/json"\s+id="serialized-server-data">([^<]+)</script>',
                html,
            )
            if m_server:
                try:
                    data = json.loads(m_server.group(1))
                    return {"type": "server_data", "payload": data}
                except Exception as exc:  # noqa: BLE001
                    logger.debug("Не удалось распарсить serialized-server-data: %s", exc)

            # 2. Попытка извлечь Schema.org ld+json
            ld_matches = re.findall(
                r'<script\s+type="application/ld\+json">([^<]+)</script>',
                html,
            )
            for raw_ld in ld_matches:
                try:
                    ld_data = json.loads(raw_ld)
                    if isinstance(ld_data, dict) and ld_data.get("@type") in (
                        "MusicAlbum",
                        "MusicPlaylist",
                        "MusicRecording",
                    ):
                        return {"type": "ld_json", "payload": ld_data}
                except Exception as exc:  # noqa: BLE001
                    logger.debug("Сбой парсинга блока ld+json: %s", exc)
                    continue

            # 3. OpenGraph fallback
            og_title = re.search(r'<meta\s+property="og:title"\s+content="([^"]+)"', html)
            og_image = re.search(r'<meta\s+property="og:image"\s+content="([^"]+)"', html)
            if og_title:
                return {
                    "type": "og",
                    "title": og_title.group(1),
                    "image": og_image.group(1) if og_image else None,
                }

            raise DownloadError("Не удалось извлечь метаданные со страницы Apple Music")
        finally:
            if should_close:
                await client.aclose()

    async def get_track(self, url: str, track_id: str) -> dict[str, Any]:
        """Извлекает метаданные трека."""
        page_info = await self.fetch_page_data(url)
        payload = page_info.get("payload")

        # Если данные из server_data
        if page_info.get("type") == "server_data" and isinstance(payload, list) and payload:
            first = payload[0] if isinstance(payload, list) else payload
            data_items = first.get("data", []) if isinstance(first, dict) else []
            for item in data_items:
                # Ищем трек по id
                if str(item.get("id")) == str(track_id):
                    attrs = item.get("attributes", {})
                    artwork = attrs.get("artwork", {})
                    return {
                        "id": track_id,
                        "title": attrs.get("name", "Unknown Track"),
                        "artist": attrs.get("artistName", "Unknown Artist").replace(" & ", "\\\\").replace(", ", "\\\\"),
                        "album": attrs.get("albumName", ""),
                        "album_artist": attrs.get("artistName", "Unknown Artist"),
                        "year": int(attrs.get("releaseDate", "0")[:4]) if attrs.get("releaseDate") else None,
                        "duration": round((attrs.get("durationInMillis") or 0) / 1000.0),
                        "cover_url": self.format_cover(artwork.get("url")),
                        "isrc": attrs.get("isrc"),
                    }
                # Проверяем вложенные треки альбома
                rel_tracks = item.get("relationships", {}).get("tracks", {}).get("data", [])
                for t in rel_tracks:
                    if str(t.get("id")) == str(track_id):
                        attrs = t.get("attributes", {})
                        artwork = attrs.get("artwork", {}) or item.get("attributes", {}).get("artwork", {})
                        return {
                            "id": track_id,
                            "title": attrs.get("name", "Unknown Track"),
                            "artist": attrs.get("artistName", "Unknown Artist").replace(" & ", "\\\\").replace(", ", "\\\\"),
                            "album": attrs.get("albumName", item.get("attributes", {}).get("name", "")),
                            "album_artist": item.get("attributes", {}).get("artistName", attrs.get("artistName")),
                            "year": int(attrs.get("releaseDate", "0")[:4]) if attrs.get("releaseDate") else None,
                            "duration": round((attrs.get("durationInMillis") or 0) / 1000.0),
                            "cover_url": self.format_cover(artwork.get("url")),
                            "isrc": attrs.get("isrc"),
                        }

        # Если данные из ld_json
        if (
            page_info.get("type") == "ld_json"
            and isinstance(payload, dict)
            and payload.get("@type") == "MusicRecording"
        ):
            by_artist = payload.get("byArtist", {})
            artist_name = by_artist.get("name", "Unknown Artist") if isinstance(by_artist, dict) else "Unknown Artist"
            dur = self.parse_iso_duration(payload.get("duration"))
            return {
                "id": track_id,
                "title": payload.get("name", "Unknown Track"),
                "artist": artist_name,
                "album": payload.get("inAlbum", {}).get("name", "") if isinstance(payload.get("inAlbum"), dict) else "",
                "album_artist": artist_name,
                "year": None,
                "duration": dur,
                "cover_url": self.format_cover(payload.get("image")),
                "isrc": None,
            }

        # Fallback по OpenGraph
        title = page_info.get("title", "Unknown Track")
        artist = "Unknown Artist"
        if " — " in title:
            title, artist = title.split(" — ", 1)
        elif " by " in title:
            title, artist = title.split(" by ", 1)
        return {
            "id": track_id,
            "title": title.strip(),
            "artist": artist.strip(),
            "album": "",
            "album_artist": artist.strip(),
            "year": None,
            "duration": 0,
            "cover_url": page_info.get("image"),
            "isrc": None,
        }

    async def get_collection(self, url: str) -> dict[str, Any]:
        """Извлекает альбом или плейлист со всеми треками."""
        page_info = await self.fetch_page_data(url)
        payload = page_info.get("payload")

        # 1. Разбор server_data
        if page_info.get("type") == "server_data" and isinstance(payload, list) and payload:
            first = payload[0]
            data_items = first.get("data", []) if isinstance(first, dict) else []
            if data_items:
                root = data_items[0]
                attrs = root.get("attributes", {})
                artwork = attrs.get("artwork", {})
                cover_url = self.format_cover(artwork.get("url"))
                title = attrs.get("name", "Unknown Collection")
                artist = attrs.get("artistName") or attrs.get("curatorName", "Apple Music")
                year = int(attrs.get("releaseDate", "0")[:4]) if attrs.get("releaseDate") else None

                tracks = []
                rel_tracks = root.get("relationships", {}).get("tracks", {}).get("data", [])
                for idx, t in enumerate(rel_tracks, start=1):
                    t_attrs = t.get("attributes", {})
                    t_art = t_attrs.get("artwork", {})
                    tracks.append({
                        "id": str(t.get("id", idx)),
                        "track_number": t_attrs.get("trackNumber", idx),
                        "title": t_attrs.get("name", "Unknown Track"),
                        "artist": (t_attrs.get("artistName") or artist).replace(" & ", "\\\\").replace(", ", "\\\\"),
                        "album": t_attrs.get("albumName", title),
                        "album_artist": artist,
                        "year": int(t_attrs.get("releaseDate", "0")[:4]) if t_attrs.get("releaseDate") else year,
                        "duration": round((t_attrs.get("durationInMillis") or 0) / 1000.0),
                        "cover_url": self.format_cover(t_art.get("url")) or cover_url,
                        "isrc": t_attrs.get("isrc"),
                    })

                return {
                    "title": title,
                    "artist": artist,
                    "year": year,
                    "cover_url": cover_url,
                    "tracks": tracks,
                }

        # 2. Разбор Schema.org ld+json
        if page_info.get("type") == "ld_json" and isinstance(payload, dict):
            title = payload.get("name", "Unknown Collection")
            by_artist = payload.get("byArtist", {})
            artist = by_artist.get("name", "Apple Music") if isinstance(by_artist, dict) else "Apple Music"
            cover_url = self.format_cover(payload.get("image"))
            tracks = []
            raw_tracks = payload.get("track", [])
            for idx, t in enumerate(raw_tracks, start=1):
                t_artist_obj = t.get("byArtist", {})
                t_artist = t_artist_obj.get("name", artist) if isinstance(t_artist_obj, dict) else artist
                tracks.append({
                    "id": str(idx),
                    "track_number": idx,
                    "title": t.get("name", "Unknown Track"),
                    "artist": t_artist,
                    "album": title,
                    "album_artist": artist,
                    "year": None,
                    "duration": self.parse_iso_duration(t.get("duration")),
                    "cover_url": cover_url,
                    "isrc": None,
                })
            return {
                "title": title,
                "artist": artist,
                "year": None,
                "cover_url": cover_url,
                "tracks": tracks,
            }

        raise NotFoundError("Не удалось обнаружить треки в коллекции Apple Music")

    async def get_artist_discography(self, url: str, artist_id: str) -> dict[str, Any]:
        """Извлекает дискографию исполнителя из страницы Apple Music."""
        page_info = await self.fetch_page_data(url)
        payload = page_info.get("payload")
        artist_name = "Unknown Artist"
        avatar_url = None
        releases: list[dict[str, Any]] = []

        if page_info.get("type") == "server_data" and isinstance(payload, list) and payload:
            first = payload[0] if isinstance(payload, list) else payload
            data_items = first.get("data", []) if isinstance(first, dict) else []
            for item in data_items:
                if item.get("type") == "artists":
                    attrs = item.get("attributes", {})
                    artist_name = attrs.get("name", artist_name)
                    artwork = attrs.get("artwork", {})
                    avatar_url = self.format_cover(artwork.get("url"))
                elif item.get("type") in ("albums", "recordings"):
                    attrs = item.get("attributes", {})
                    rel_title = attrs.get("name")
                    if rel_title:
                        artwork = attrs.get("artwork", {})
                        rel_cover = self.format_cover(artwork.get("url"))
                        year_val = None
                        if attrs.get("releaseDate"):
                            try:
                                year_val = int(str(attrs["releaseDate"])[:4])
                            except ValueError:
                                pass
                        releases.append({
                            "id": str(item.get("id")),
                            "title": rel_title,
                            "type": "single" if attrs.get("isSingle") else "album",
                            "year": year_val,
                            "track_count": attrs.get("trackCount"),
                            "cover_url": rel_cover,
                            "url": attrs.get("url") or f"https://music.apple.com/album/{item.get('id')}",
                        })

        if not artist_name or artist_name == "Unknown Artist":
            og_title = page_info.get("title")
            if og_title:
                artist_name = og_title.split(" on Apple Music")[0].split(" в Apple Music")[0].strip()
            if page_info.get("image"):
                avatar_url = self.format_cover(page_info.get("image"))

        return {
            "artist_name": artist_name,
            "artist_avatar": avatar_url,
            "releases": releases,
        }
