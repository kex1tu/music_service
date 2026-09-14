"""Сервис резолва источников и сопоставления метаданных (Source Resolver & Audio Matcher).

Определяет тип источника (SoundCloud, YouTube, Deezer), извлекает
метаданные и связывает их с аудиопотоками. Для треков Deezer использует
алгоритм нечеткого поиска (RapidFuzz) и сравнения таймингов для подбора
наилучшего официального аудиопотока в YouTube Music / YouTube.
"""

import asyncio
import logging
import math
import re
import urllib.parse
from pathlib import Path
from typing import Any, Literal

import yt_dlp
from rapidfuzz import fuzz, utils

from app.config import Settings
from app.core.exceptions import AudioMatchError, UnsupportedSourceError
from app.schemas.download import (
    ArtistDiscographyResponse,
    DiscographyReleaseItem,
    ResolvedTrack,
)
from app.services.apple_music_client import AppleMusicClient
from app.services.deezer_client import DeezerClient
from app.services.spotify_client import SpotifyClient
from app.services.yandex_client import YandexMusicClient

logger = logging.getLogger("music_manager.resolver")


def safe_int_duration(val: Any) -> int:
    """Безопасно конвертирует длительность в целое неотрицательное число секунд."""
    if val is None:
        return 0
    try:
        f = float(val)
        if math.isnan(f) or math.isinf(f) or f < 0:
            return 0
        return max(0, round(f))
    except (ValueError, TypeError, OverflowError):
        return 0


def normalize_soundcloud_artwork(art_url: str | None) -> str | None:
    """Улучшает разрешение обложки SoundCloud до t500x500."""
    if not art_url:
        return None
    return re.sub(
        r"-(?:large|badge|small|tiny|mini)\.([a-zA-Z0-9]+)(?:(\?.*)|$)",
        r"-t500x500.\1\2",
        art_url,
    )


def _patch_soundcloud_extractor() -> None:
    """Патчит yt-dlp SoundcloudPlaylistBaseIE для быстрого и полного извлечения треков плейлиста."""
    try:
        from yt_dlp.extractor.soundcloud import SoundcloudIE, SoundcloudPlaylistBaseIE
        from yt_dlp.utils import (
            float_or_none,
            int_or_none,
            str_or_none,
            traverse_obj,
            unified_timestamp,
            url_or_none,
        )

        if getattr(SoundcloudPlaylistBaseIE, "_patched_for_music_manager", False):
            return

        def _patched_extract_set(self, playlist, token=None):
            playlist_id = str(playlist.get("id", ""))
            tracks = playlist.get("tracks") or []
            missing: list[str] = []
            for t in tracks:
                if isinstance(t, dict):
                    if t.get("id") and (not t.get("permalink_url") or not t.get("title")):
                        missing.append(str(t["id"]))
                elif isinstance(t, (int, str)) and str(t).isdigit():
                    missing.append(str(t))
            if missing:
                fetched: dict[str, Any] = {}
                for i in range(0, len(missing), 50):
                    batch = missing[i : i + 50]
                    query = {"ids": ",".join(batch)}
                    if token:
                        query["playlistId"] = playlist_id
                        query["playlistSecretToken"] = token
                    try:
                        batch_res = self._call_api(
                            self._API_V2_BASE + "tracks",
                            playlist_id,
                            "Downloading tracks",
                            query=query,
                            headers=self._HEADERS,
                        )
                        if isinstance(batch_res, dict):
                            batch_tracks = (
                                batch_res.get("collection")
                                or batch_res.get("tracks")
                                or batch_res.get("data")
                                or batch_res.get("items")
                                or []
                            )
                        elif isinstance(batch_res, (list, tuple)):
                            batch_tracks = batch_res
                        else:
                            batch_tracks = []

                        for bt in batch_tracks:
                            if bt and isinstance(bt, dict) and bt.get("id"):
                                fetched[str(bt["id"])] = bt
                    except Exception as exc:  # noqa: BLE001
                        logger.warning("Не удалось дозагрузить треки SoundCloud: %s", exc)
                        continue

                full_tracks = []
                for t in tracks:
                    if isinstance(t, dict):
                        tid = str(t.get("id"))
                        if tid in fetched and isinstance(fetched[tid], dict):
                            merged = dict(t)
                            merged.update(fetched[tid])
                            full_tracks.append(merged)
                        else:
                            full_tracks.append(t)
                    elif isinstance(t, (int, str)) and str(t) in fetched and isinstance(fetched[str(t)], dict):
                        full_tracks.append(fetched[str(t)])
                    else:
                        full_tracks.append(t)
                tracks = full_tracks

            album_info = traverse_obj(
                playlist,
                {
                    "album": ("title", {str}),
                    "album_artist": ("user", "username", {str}),
                    "album_type": ("set_type", {str}, {lambda x: x or "playlist"}),
                },
            )
            entries = []
            for track in tracks:
                if not track or not isinstance(track, dict):
                    continue
                track_id = str_or_none(track.get("id"))
                url = track.get("permalink_url")
                track_secret = track.get("secret_token") or token
                if not url:
                    if not track_id:
                        continue
                    url = self._API_V2_BASE + "tracks/" + track_id
                    if track_secret:
                        url += "?secret_token=" + track_secret
                elif track_secret and track_secret not in url:
                    sep = "&" if "?" in url else "?"
                    url += f"{sep}secret_token={track_secret}"

                t_title = track.get("title")
                t_user = traverse_obj(track, ("user", "username", {str}))
                t_artist = traverse_obj(track, ("publisher_metadata", "artist", {str})) or t_user
                t_duration = float_or_none(track.get("duration"), scale=1000)

                t_art = track.get("artwork_url") or traverse_obj(track, ("user", "avatar_url", {str}))
                t_art = normalize_soundcloud_artwork(t_art)

                entries.append(
                    self.url_result(
                        url,
                        SoundcloudIE.ie_key(),
                        track_id,
                        url_transparent=True,
                        video_title=t_title,
                        uploader=t_user,
                        artist=t_artist,
                        duration=t_duration,
                        thumbnail=t_art,
                        **album_info,
                    )
                )

            return self.playlist_result(
                entries,
                playlist_id,
                playlist.get("title"),
                playlist.get("description"),
                **album_info,
                **traverse_obj(
                    playlist,
                    {
                        "uploader": ("user", "username", {str}),
                        "uploader_id": ("user", "id", {str_or_none}),
                        "uploader_url": ("user", "permalink_url", {url_or_none}),
                        "timestamp": ("created_at", {unified_timestamp}),
                        "release_timestamp": (("release_date", "published_at"), {unified_timestamp}, any),
                        "modified_timestamp": ("last_modified", {unified_timestamp}),
                        "duration": ("duration", {float_or_none(scale=1000)}),
                        "license": ("license", {str}),
                        "like_count": ("likes_count", {int_or_none}),
                        "repost_count": ("reposts_count", {int_or_none}),
                        "genres": ("genre", {str}, filter, all, filter),
                        "tags": ("tag_list", {self._TAGS_RE.findall}, ..., ..., filter),
                    },
                ),
                thumbnails=self._extract_thumbnails(playlist),
            )

        SoundcloudPlaylistBaseIE._extract_set = _patched_extract_set
        SoundcloudPlaylistBaseIE._patched_for_music_manager = True
    except Exception as exc:  # noqa: BLE001
        logger.warning("Не удалось применить патч SoundCloud extractor: %s", exc)


_patch_soundcloud_extractor()

BAD_TITLE_KEYWORDS = [
    "live",
    "concert",
    "cover",
    "tribute",
    "karaoke",
    "instrumental",
    "slowed",
    "reverb",
    "8d audio",
    "bass boosted",
    "reaction",
    "guitar cover",
    "drum cover",
    "tutorial",
    "remix",
]


class AudioMatcher:
    """Сопоставитель метаданных студийного трека с аудиопотоками YouTube."""

    def __init__(
        self,
        ytdl_opts: dict[str, Any] | None = None,
        cookies_path: Path | None = None,
    ) -> None:
        self._cookies_path = cookies_path
        self._ytdl_opts = ytdl_opts or {
            "extract_flat": True,
            "skip_download": True,
            "quiet": True,
            "no_warnings": True,
            "simulate": True,
            "ignoreconfig": True,
            "http_headers": {
                "Accept-Language": "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7",
            },
            "extractor_args": {
                "youtube": {
                    "lang": ["ru"],
                }
            },
        }
        if self._cookies_path and self._cookies_path.is_file():
            self._ytdl_opts["cookiefile"] = str(self._cookies_path)

    @staticmethod
    def clean_query_text(text: str) -> str:
        """Удаляет спецсимволы и пунктуацию, сохраняя буквенно-цифровые токены."""
        normalized = (
            text.replace("’", "'")
            .replace("“", '"')
            .replace("”", '"')
            .replace("–", "-")
            .replace("—", "-")
        )
        cleaned = re.sub(r'[\/\\:\*\?"<>\|!~@#\$%\^&\+=\[\]\{\}]', " ", normalized)
        return " ".join(cleaned.split())

    @staticmethod
    def strip_artist_prefix(title: str, artist: str) -> str:
        """Устраняет дублирование артиста в названии трека (например, 'Artist - Title')."""
        clean_art = artist.strip().lower()
        clean_tit = title.strip()
        if clean_tit.lower().startswith(f"{clean_art} - ") or clean_tit.lower().startswith(
            f"{clean_art} -"
        ):
            return clean_tit[len(clean_art) :].lstrip(" -").strip()
        return clean_tit

    @staticmethod
    def calculate_score(
        target_title: str,
        target_artist: str,
        target_duration: int,
        candidate_title: str,
        candidate_channel: str,
        candidate_duration: int,
    ) -> float:
        """Вычисляет оценку соответствия кандидата целевому треку (0.0 — 100.0).

        Критерии:
        1. Длительность: совпадение +-2 сек дает максимум баллов (+40).
        2. Название: сходство через token_set_ratio (+30).
        3. Исполнитель: наличие в названии/канале (+15).
        4. Бонус за официальный канал (" - Topic", "VEVO", "Official") (+15).
        5. Штраф за слова live, cover, remix, если их нет в оригинале (-35).
        """
        score = 0.0

        # 1. Оценка длительности (макс. 40 баллов)
        if target_duration > 0 and candidate_duration > 0:
            diff = abs(candidate_duration - target_duration)
            if diff <= 2:
                score += 40.0
            elif diff <= 5:
                score += 25.0
            elif diff <= 12:
                score += 10.0
            elif diff > 30:
                score -= 40.0
        else:
            score += 15.0  # Длительность неизвестна

        # 2. Оценка названия трека (макс. 30 баллов)
        base_title = re.sub(r"[\(\[\{].*?[\)\]\}]", "", target_title).strip()
        t_sim1 = fuzz.token_set_ratio(
            target_title, candidate_title, processor=utils.default_process
        )
        t_sim2 = (
            fuzz.token_set_ratio(base_title, candidate_title, processor=utils.default_process)
            if base_title
            else 0.0
        )
        title_sim = max(t_sim1, t_sim2)
        score += (title_sim / 100.0) * 30.0

        # 3. Оценка исполнителя (макс. 15 баллов)
        artist_sim_channel = fuzz.partial_ratio(
            target_artist, candidate_channel, processor=utils.default_process
        )
        artist_sim_title = fuzz.partial_ratio(
            target_artist, candidate_title, processor=utils.default_process
        )
        best_artist_sim = max(artist_sim_channel, artist_sim_title)
        score += (best_artist_sim / 100.0) * 15.0

        # 4. Бонус за официальный источник (макс. 15 баллов)
        ch_lower = candidate_channel.lower()
        artist_channel_sim = fuzz.ratio(
            target_artist, candidate_channel, processor=utils.default_process
        )
        if ch_lower.endswith(" - topic"):
            score += 15.0  # Официальные треки YouTube Music
        elif "vevo" in ch_lower or "official" in ch_lower or artist_channel_sim >= 85.0:
            score += 10.0

        # 5. Штрафы за нежелательные модификации с контролем границ слов (\b)
        target_lower = target_title.lower()
        candidate_lower = candidate_title.lower()
        for kw in BAD_TITLE_KEYWORDS:
            pattern = rf"\b{re.escape(kw)}\b"
            if re.search(pattern, candidate_lower) and not re.search(pattern, target_lower):
                score -= 35.0
                break

        return max(0.0, min(100.0, score))

    async def find_best_match(
        self,
        title: str,
        artist: str,
        duration: int,
        cobalt_fallback_url: str | None = None,
    ) -> str:
        """Ищет наилучший аудиопоток на YouTube/YouTube Music для заданного трека.

        Возвращает прямой URL на ролик YouTube.
        """
        clean_art = self.clean_query_text(artist)
        clean_tit = self.clean_query_text(self.strip_artist_prefix(title, artist))
        base_tit = re.sub(r"[\(\[\{].*?[\)\]\}]", "", clean_tit).strip() or clean_tit
        alnum_art = re.sub(r"[^\w\s]", " ", clean_art).strip()
        alnum_tit = re.sub(r"[^\w\s]", " ", base_tit).strip()

        # 6-этапная поисковая лестница
        queries = [
            f"ytsearch5:{clean_art} {clean_tit} Official Audio",
            f"ytsearch5:{clean_art} {base_tit} Topic",
            f"ytsearch5:{clean_art} {clean_tit}",
            f"ytsearch5:{clean_art} {clean_tit} Official",
            f"ytsearch5:{clean_art} {clean_tit} Audio",
        ]
        if alnum_art != clean_art or alnum_tit != clean_tit:
            queries.append(f"ytsearch5:{alnum_art} {alnum_tit}")

        # Устраняем дубликаты с сохранением порядка
        seen_queries: set[str] = set()
        unique_queries: list[str] = []
        for q in queries:
            if q not in seen_queries:
                seen_queries.add(q)
                unique_queries.append(q)

        best_candidate: dict[str, Any] | None = None
        best_score = -1.0
        EARLY_ACCEPT_SCORE = 65.0

        for q in unique_queries:
            def _search(search_q: str) -> list[dict[str, Any]]:
                if search_q.startswith("ytmsearch"):
                    q_text = search_q.split(":", 1)[-1]
                    search_q = f"https://music.youtube.com/search?q={urllib.parse.quote(q_text)}#songs"
                guest_opts = {k: v for k, v in self._ytdl_opts.items() if k != "cookiefile"}
                try:
                    with yt_dlp.YoutubeDL(guest_opts) as ydl:
                        info = ydl.extract_info(search_q, download=False)
                        return (info.get("entries", []) or []) if info else []
                except Exception as exc:
                    if "cookiefile" in self._ytdl_opts:
                        logger.debug("Гостевой поиск не удался (%s), повтор с cookies...", exc)
                        with yt_dlp.YoutubeDL(self._ytdl_opts) as ydl:
                            info = ydl.extract_info(search_q, download=False)
                            return (info.get("entries", []) or []) if info else []
                    raise

            try:
                entries = await asyncio.to_thread(_search, q)
            except Exception as exc:  # noqa: BLE001
                logger.warning("Ошибка поиска yt-dlp по запросу '%s': %s", q, exc)
                continue

            for candidate in entries:
                if not candidate:
                    continue
                c_title = candidate.get("title", "")
                c_channel = candidate.get("channel") or candidate.get("uploader") or ""
                c_duration = int(candidate.get("duration") or 0)

                score = self.calculate_score(
                    target_title=title,
                    target_artist=artist,
                    target_duration=duration,
                    candidate_title=c_title,
                    candidate_channel=c_channel,
                    candidate_duration=c_duration,
                )
                logger.debug(
                    "Кандидат '%s' [%s, %ds]: Score=%.1f (запрос: %s)",
                    c_title,
                    c_channel,
                    c_duration,
                    score,
                    q,
                )
                if score > best_score:
                    best_score = score
                    best_candidate = candidate

            # Быстрый выход при высоком качестве совпадения
            if best_score >= EARLY_ACCEPT_SCORE:
                break

        if best_candidate and best_score >= 45.0:
            video_id = best_candidate.get("id")
            url = best_candidate.get("url")
            if video_id:
                return f"https://www.youtube.com/watch?v={video_id}"
            if url:
                return url

        if best_candidate and best_score >= 25.0:
            video_id = best_candidate.get("id")
            url = best_candidate.get("url")
            logger.warning(
                "Низкий скор (%.1f) для трека '%s - %s', выбран кандидат '%s'",
                best_score,
                artist,
                title,
                best_candidate.get("title"),
            )
            if video_id:
                return f"https://www.youtube.com/watch?v={video_id}"
            if url:
                return url

        raise AudioMatchError(
            f"Не найдено подходящего совпадения для трека '{artist} - {title}' (макс. скор: {best_score:.1f})"
        )


class SourceResolver:
    """Резолвер источников и формирователь нормализованных метаданных."""

    def __init__(
        self,
        deezer_client: DeezerClient,
        audio_matcher: AudioMatcher | None = None,
        cobalt_api_url: str | None = None,
        allowed_domains: tuple[str, ...] = (
            "youtube.com",
            "music.youtube.com",
            "youtu.be",
            "soundcloud.com",
            "on.soundcloud.com",
            "deezer.com",
            "deezer.page.link",
            "link.deezer.com",
            "music.yandex.ru",
            "music.yandex.com",
            "music.yandex.by",
            "music.yandex.kz",
            "open.spotify.com",
            "spotify.link",
            "music.apple.com",
        ),
        cookies_path: Path | None = None,
        settings: Settings | None = None,
        yandex_client: YandexMusicClient | None = None,
        spotify_client: SpotifyClient | None = None,
        apple_music_client: AppleMusicClient | None = None,
    ) -> None:
        self._deezer = deezer_client
        self._cookies_path = cookies_path
        self._matcher = audio_matcher or AudioMatcher(cookies_path=cookies_path)
        self._cobalt_api_url = cobalt_api_url
        self._allowed_domains = allowed_domains
        if settings is not None:
            self._settings = settings
        else:
            try:
                self._settings = Settings()
            except Exception:  # noqa: BLE001
                class _FallbackSettings:
                    MATCHER_CONCURRENCY = 4
                    YANDEX_MUSIC_TOKEN = None
                    SPOTIFY_CLIENT_ID = None
                    SPOTIFY_CLIENT_SECRET = None
                self._settings = _FallbackSettings()  # type: ignore[assignment]

        self._yandex = yandex_client or YandexMusicClient(
            token=getattr(self._settings, "YANDEX_MUSIC_TOKEN", None)
        )
        self._spotify = spotify_client or SpotifyClient(
            client_id=getattr(self._settings, "SPOTIFY_CLIENT_ID", None),
            client_secret=getattr(self._settings, "SPOTIFY_CLIENT_SECRET", None),
        )
        self._apple = apple_music_client or AppleMusicClient()

    def detect_source(
        self, url: str
    ) -> Literal["soundcloud", "youtube", "deezer", "yandex", "spotify", "applemusic", "torrent"]:
        """Проверяет URL по белому списку доменов и возвращает тип источника.

        Raises:
            UnsupportedSourceError: Если домен не входит в список разрешенных.
        """
        # Торрент-источники: проверяем протокол magnet или путь/URL к файлу .torrent
        url_clean = url.strip()
        url_lower = url_clean.lower()
        if url_lower.startswith("magnet:"):
            return "torrent"

        if (
            url_lower.endswith(".torrent")
            or ".torrent?" in url_lower
            or "/_torrents/" in url_clean
        ):
            return "torrent"

        if not re.match(r"^https?://", url_clean, re.IGNORECASE):
            url_clean = f"https://{url_clean}"
        parsed = urllib.parse.urlparse(url_clean)
        hostname = (parsed.hostname or "").lower()

        is_allowed = any(
            hostname == domain or hostname.endswith(f".{domain}")
            for domain in self._allowed_domains
        )
        if not is_allowed:
            raise UnsupportedSourceError(
                f"Домен '{hostname}' не входит в белый список разрешенных источников"
            )

        if "deezer.com" in hostname or "deezer.page.link" in hostname:
            return "deezer"
        if "music.yandex." in hostname or hostname == "music.yandex.ru":
            return "yandex"
        if "spotify.com" in hostname or "spotify.link" in hostname:
            return "spotify"
        if "apple.com" in hostname:
            return "applemusic"
        if "soundcloud.com" in hostname:
            return "soundcloud"
        if "youtube.com" in hostname or "youtu.be" in hostname:
            return "youtube"

        raise UnsupportedSourceError(f"Не удалось сопоставить домен '{hostname}' с источником")

    async def resolve(
        self,
        url: str,
        source_hint: str = "auto",
    ) -> tuple[dict[str, Any], list[ResolvedTrack]]:
        """Разрешает URL в метаданные релиза и список ResolvedTrack с источниками аудио.

        Returns:
            Кортеж (metadata_dict, tracks_list)
        """
        source = self.detect_source(url) if source_hint == "auto" else source_hint

        if source == "deezer":
            return await self._resolve_deezer(url)
        if source == "yandex":
            return await self._resolve_yandex(url)
        if source == "spotify":
            return await self._resolve_spotify(url)
        if source == "applemusic":
            return await self._resolve_apple_music(url)
        if source in ("youtube", "soundcloud"):
            return await self._resolve_ytdlp(url, source)

        if source == "torrent":
            # Торренты обрабатываются напрямую через TorrentService в DownloadService;
            # возвращаем маркер отложенного разрешения метаданных.
            return {"album": "", "album_artist": "", "year": None, "cover_url": None}, []

        raise UnsupportedSourceError(f"Неподдерживаемый источник: {source}")

    async def resolve_artist_discography(self, url: str) -> ArtistDiscographyResponse:
        """Разрешает URL исполнителя в структурированную дискографию со списком релизов."""
        source = self.detect_source(url)

        if source == "deezer":
            res_type, res_id = await self._deezer.parse_url(url)
            if res_type != "artist":
                raise UnsupportedSourceError("Ссылка Deezer должна вести на профиль артиста (/artist/{id})")
            artist_data = await self._deezer.get_artist(res_id)
            albums_data = await self._deezer.get_artist_albums(res_id, limit=150)
            releases: list[DiscographyReleaseItem] = []
            for alb in albums_data:
                rec_type = alb.get("record_type", "album").lower()
                year = None
                rel_date = alb.get("release_date")
                if rel_date:
                    try:
                        year = int(str(rel_date)[:4])
                    except ValueError:
                        pass
                releases.append(
                    DiscographyReleaseItem(
                        id=str(alb.get("id")),
                        title=alb.get("title", "Unknown Release"),
                        type=rec_type,
                        year=year,
                        track_count=alb.get("nb_tracks"),
                        cover_url=alb.get("cover_xl") or alb.get("cover_big") or alb.get("cover_medium"),
                        url=alb.get("link") or f"https://www.deezer.com/album/{alb.get('id')}",
                    )
                )
            return ArtistDiscographyResponse(
                artist_id=str(res_id),
                artist_name=artist_data.get("name", "Unknown Artist"),
                artist_avatar=artist_data.get("picture_xl") or artist_data.get("picture_big") or artist_data.get("picture_medium"),
                source="deezer",
                total_releases=len(releases),
                releases=releases,
            )

        if source == "yandex":
            res_type, params = await self._yandex.parse_url(url)
            if res_type != "artist":
                raise UnsupportedSourceError("Ссылка Яндекс Музыки должна вести на профиль артиста (/artist/{id})")
            artist_id = params["artist_id"]
            data = await self._yandex.get_artist_discography(artist_id)
            artist_obj = data.get("artist", {})
            albums_data = data.get("albums", [])
            releases = []
            for alb in albums_data:
                alb_type = alb.get("type", "album").lower()
                releases.append(
                    DiscographyReleaseItem(
                        id=str(alb.get("id")),
                        title=alb.get("title", "Unknown Release"),
                        type=alb_type,
                        year=alb.get("year"),
                        track_count=alb.get("trackCount"),
                        cover_url=self._yandex.format_cover(alb.get("coverUri")),
                        url=f"https://music.yandex.ru/album/{alb.get('id')}",
                    )
                )
            cover_obj = artist_obj.get("cover", {})
            avatar_url = self._yandex.format_cover(cover_obj.get("uri")) if isinstance(cover_obj, dict) else None
            return ArtistDiscographyResponse(
                artist_id=str(artist_id),
                artist_name=artist_obj.get("name", "Unknown Artist"),
                artist_avatar=avatar_url,
                source="yandex",
                total_releases=len(releases),
                releases=releases,
            )

        if source == "spotify":
            res_type, res_id = await self._spotify.parse_url(url)
            if res_type != "artist":
                raise UnsupportedSourceError("Ссылка Spotify должна вести на профиль артиста (/artist/{id})")
            data = await self._spotify.get_artist_discography(res_id)
            artist_obj = data.get("artist", {})
            albums_data = data.get("items", [])
            releases = []
            for item in albums_data:
                images = item.get("images", [])
                cover = images[0].get("url") if images else None
                year = None
                rel_date = item.get("release_date")
                if rel_date:
                    try:
                        year = int(str(rel_date)[:4])
                    except ValueError:
                        pass
                releases.append(
                    DiscographyReleaseItem(
                        id=str(item.get("id")),
                        title=item.get("name", "Unknown Release"),
                        type=item.get("album_type", "album").lower(),
                        year=year,
                        track_count=item.get("total_tracks"),
                        cover_url=cover,
                        url=item.get("external_urls", {}).get("spotify") or f"https://open.spotify.com/album/{item.get('id')}",
                    )
                )
            artist_images = artist_obj.get("images", [])
            avatar_url = artist_images[0].get("url") if artist_images else None
            artist_name = artist_obj.get("name", "Unknown Artist")

            # Fallback к поиску по каталогу Deezer, если со Spotify не удалось извлечь релизы
            if not releases and artist_name and artist_name != "Unknown Artist":
                logger.info(
                    "Spotify discography empty for %s, falling back to Deezer artist search",
                    artist_name,
                )
                try:
                    search_res = await self._deezer._request_api("/search/artist", params={"q": artist_name, "limit": 5})
                    deezer_artists = search_res.get("data", [])
                    best_match = None
                    for a in deezer_artists:
                        if a.get("name", "").strip().lower() == artist_name.strip().lower():
                            best_match = a
                            break
                    if not best_match and deezer_artists:
                        best_match = max(deezer_artists, key=lambda a: a.get("nb_album", 0))

                    if best_match:
                        d_albums = await self._deezer.get_artist_albums(str(best_match["id"]), limit=150)
                        for alb in d_albums:
                            rec_type = alb.get("record_type", "album").lower()
                            y = None
                            if alb.get("release_date"):
                                try:
                                    y = int(str(alb.get("release_date"))[:4])
                                except ValueError:
                                    pass
                            releases.append(
                                DiscographyReleaseItem(
                                    id=str(alb.get("id")),
                                    title=alb.get("title", "Unknown Release"),
                                    type=rec_type,
                                    year=y,
                                    track_count=alb.get("nb_tracks"),
                                    cover_url=alb.get("cover_xl") or alb.get("cover_big") or alb.get("cover_medium"),
                                    url=alb.get("link") or f"https://www.deezer.com/album/{alb.get('id')}",
                                )
                            )
                        if not avatar_url:
                            avatar_url = best_match.get("picture_xl") or best_match.get("picture_big")
                except Exception as exc:  # noqa: BLE001
                    logger.warning("Deezer fallback for Spotify artist %s failed: %s", artist_name, exc)

            return ArtistDiscographyResponse(
                artist_id=str(res_id),
                artist_name=artist_name,
                artist_avatar=avatar_url,
                source="spotify",
                total_releases=len(releases),
                releases=releases,
            )

        if source == "applemusic":
            res_type, params = self._apple.parse_url(url)
            if res_type != "artist":
                raise UnsupportedSourceError("Ссылка Apple Music должна вести на профиль артиста (/artist/{id})")
            artist_id = params.get("artist_id", "")
            data = await self._apple.get_artist_discography(url, artist_id)
            releases = [
                DiscographyReleaseItem(
                    id=str(rel.get("id")),
                    title=rel.get("title", "Unknown Release"),
                    type=rel.get("type", "album"),
                    year=rel.get("year"),
                    track_count=rel.get("track_count"),
                    cover_url=rel.get("cover_url"),
                    url=rel.get("url"),
                )
                for rel in data.get("releases", [])
            ]
            return ArtistDiscographyResponse(
                artist_id=str(artist_id),
                artist_name=data.get("artist_name", "Unknown Artist"),
                artist_avatar=data.get("artist_avatar"),
                source="applemusic",
                total_releases=len(releases),
                releases=releases,
            )

        if source in ("youtube", "soundcloud"):
            return await self._resolve_ytdlp_artist_discography(url, source)

        raise UnsupportedSourceError(f"Получение дискографии не поддерживается для источника: {source}")

    async def _resolve_ytdlp_artist_discography(self, url: str, source: str) -> ArtistDiscographyResponse:
        """Извлекает релизы исполнителя YouTube / SoundCloud через yt-dlp."""
        def _extract(target_url: str) -> dict[str, Any]:
            opts = {
                "extract_flat": True,
                "skip_download": True,
                "quiet": True,
                "no_warnings": True,
                "ignoreconfig": True,
                "http_headers": {
                    "Accept-Language": "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7",
                },
                "extractor_args": {
                    "youtube": {
                        "lang": ["ru"],
                    }
                },
            }
            if self._cookies_path and self._cookies_path.is_file():
                opts["cookiefile"] = str(self._cookies_path)
            with yt_dlp.YoutubeDL(opts) as ydl:
                return ydl.extract_info(target_url, download=False) or {}

        info = await asyncio.to_thread(_extract, url)
        channel_name = info.get("channel") or info.get("uploader") or info.get("title") or "Unknown Channel"
        avatar = info.get("thumbnail")
        entries = list(info.get("entries") or [])

        releases = []
        for idx, entry in enumerate(entries, start=1):
            e_id = entry.get("id") or str(idx)
            e_title = entry.get("title") or f"Release {idx}"
            e_url = entry.get("url") or entry.get("webpage_url") or f"https://www.youtube.com/playlist?list={e_id}"
            e_cover = entry.get("thumbnail") or avatar
            releases.append(
                DiscographyReleaseItem(
                    id=str(e_id),
                    title=e_title,
                    type="album",
                    year=None,
                    track_count=entry.get("playlist_count"),
                    cover_url=e_cover,
                    url=e_url,
                )
            )

        return ArtistDiscographyResponse(
            artist_id=str(info.get("channel_id") or info.get("id") or "channel"),
            artist_name=channel_name,
            artist_avatar=avatar,
            source=source,
            total_releases=len(releases),
            releases=releases,
        )

    async def _resolve_deezer(self, url: str) -> tuple[dict[str, Any], list[ResolvedTrack]]:
        """Разрешает Deezer URL: извлекает метаданные и связывает аудио через AudioMatcher."""
        res_type, res_id = await self._deezer.parse_url(url)

        if res_type == "track":
            data = await self._deezer.get_track(res_id)
            album_info = data.get("album", {})
            artists_str = DeezerClient.format_artists(data)
            title = data.get("title", "Unknown Track")
            duration = safe_int_duration(data.get("duration"))
            cover_url = DeezerClient.extract_best_cover_url(data)

            # Извлекаем детальные метаданные альбома из Deezer API при наличии album_id
            album_id = album_info.get("id")
            album_data: dict[str, Any] = {}
            if album_id:
                try:
                    album_data = await self._deezer.get_album(str(album_id))
                except Exception as exc:  # noqa: BLE001
                    logger.debug("Не удалось получить полные данные альбома %s: %s", album_id, exc)

            album_title = (
                album_data.get("title")
                or album_info.get("title")
                or title
            )
            album_artist = (
                DeezerClient.format_artists(album_data)
                if album_data
                else (artists_str or "Unknown Artist")
            )

            # Извлекаем дату релиза
            year_val = None
            release_date = (
                album_data.get("release_date")
                or album_info.get("release_date")
                or data.get("release_date")
            )
            if release_date:
                m = re.match(r"^(\d{4})", str(release_date))
                if m:
                    year_val = int(m.group(1))

            # Извлекаем жанры
            genre_name = None
            genres_data = album_data.get("genres", {})
            genres_list = (
                genres_data.get("data", [])
                if isinstance(genres_data, dict)
                else (genres_data if isinstance(genres_data, list) else [])
            )
            if genres_list and isinstance(genres_list, list):
                genre_names = [g.get("name") for g in genres_list if isinstance(g, dict) and g.get("name")]
                if genre_names:
                    genre_name = "\\\\".join(genre_names)

            track_pos = int(data.get("track_position") or 1)
            disk_num = int(data.get("disk_number") or 1)
            total_tracks = int(album_data.get("nb_tracks") or 1)
            disc_total = 1
            if album_data.get("tracks", {}).get("data"):
                disc_total = max(
                    (int(t.get("disk_number") or 1) for t in album_data["tracks"]["data"]),
                    default=1,
                )

            if not cover_url:
                cover_url = DeezerClient.extract_best_cover_url(album_data)

            audio_url = await self._matcher.find_best_match(
                title=title,
                artist=artists_str.replace("\\\\", " "),
                duration=duration,
                cobalt_fallback_url=self._cobalt_api_url,
            )

            track = ResolvedTrack(
                track_id=str(data.get("id", res_id)),
                track_number=track_pos,
                track_total=total_tracks,
                disc_number=disk_num,
                disc_total=disc_total,
                title=title,
                artist=artists_str,
                album=album_title,
                album_artist=album_artist,
                year=year_val,
                genre=genre_name,
                duration=duration,
                isrc=data.get("isrc"),
                cover_url=cover_url,
                audio_source_url=audio_url,
            )

            album_meta = {
                "album": track.album,
                "album_artist": track.album_artist or artists_str,
                "year": track.year,
                "genre": track.genre,
                "disc_total": disc_total,
                "track_total": total_tracks,
                "cover_url": cover_url,
            }
            return album_meta, [track]

        if res_type in ("album", "playlist"):
            data = (
                await self._deezer.get_album(res_id, fetch_track_details=True)
                if res_type == "album"
                else await self._deezer.get_playlist(res_id, fetch_track_details=True)
            )

            is_album = res_type == "album"
            album_title = data.get("title", "Unknown Album")
            album_artist = DeezerClient.format_artists(data)
            year_val = None
            release_date = data.get("release_date")
            if release_date:
                m = re.match(r"^(\d{4})", str(release_date))
                if m:
                    year_val = int(m.group(1))

            genre_name = None
            genres_data = data.get("genres", {})
            genres_list = (
                genres_data.get("data", [])
                if isinstance(genres_data, dict)
                else (genres_data if isinstance(genres_data, list) else [])
            )
            if genres_list and isinstance(genres_list, list):
                genre_names = [g.get("name") for g in genres_list if isinstance(g, dict) and g.get("name")]
                if genre_names:
                    genre_name = "\\\\".join(genre_names)

            cover_url = DeezerClient.extract_best_cover_url(data)
            raw_tracks = data.get("tracks", {}).get("data", [])
            total_tracks_count = int(data.get("nb_tracks") or len(raw_tracks) or 1)
            disc_total_val = max(
                (int(t.get("disk_number") or 1) for t in raw_tracks),
                default=1,
            )

            semaphore = asyncio.Semaphore(self._settings.MATCHER_CONCURRENCY)

            async def _resolve_single_track(item: dict[str, Any], default_pos: int) -> ResolvedTrack:
                t_title = item.get("title", "Unknown Track")
                t_artists = DeezerClient.format_artists(item)
                if not t_artists or t_artists == "Unknown Artist":
                    t_artists = album_artist
                t_duration = safe_int_duration(item.get("duration"))
                t_disk = int(item.get("disk_number") or 1)
                t_pos = int(item.get("track_position") or default_pos)

                if is_album:
                    t_album = album_title
                    t_album_artist = album_artist
                    t_cover = cover_url
                    t_year = year_val
                    t_genre = genre_name
                    t_track_total = total_tracks_count
                    t_disc_total = disc_total_val
                else:
                    # Плейлисты: каждый трек содержит собственные метаданные альбома
                    t_album_obj = item.get("album", {})
                    t_album = t_album_obj.get("title") or album_title
                    t_album_artist = (
                        t_album_obj.get("artist", {}).get("name")
                        if isinstance(t_album_obj.get("artist"), dict)
                        else (item.get("artist", {}).get("name") or t_artists)
                    )
                    t_cover = DeezerClient.extract_best_cover_url(t_album_obj) or cover_url
                    t_year = None
                    t_rel_date = t_album_obj.get("release_date") or item.get("release_date")
                    if t_rel_date:
                        m_t = re.match(r"^(\d{4})", str(t_rel_date))
                        if m_t:
                            t_year = int(m_t.group(1))
                    t_genre = genre_name
                    t_track_total = None
                    t_disc_total = 1

                audio_url = ""
                track_status = "pending"
                error_msg: str | None = None

                try:
                    async with semaphore:
                        audio_url = await self._matcher.find_best_match(
                            title=t_title,
                            artist=t_artists.replace("\\\\", " "),
                            duration=t_duration,
                            cobalt_fallback_url=self._cobalt_api_url,
                        )
                except Exception as exc:  # noqa: BLE001
                    logger.warning("Не удалось сопоставить трек '%s - %s': %s", t_artists, t_title, exc)
                    track_status = "error"
                    error_msg = str(exc)
                    audio_url = ""

                return ResolvedTrack(
                    track_id=str(item.get("id", default_pos)),
                    track_number=t_pos,
                    track_total=t_track_total,
                    disc_number=t_disk,
                    disc_total=t_disc_total,
                    title=t_title,
                    artist=t_artists,
                    album=t_album,
                    album_artist=t_album_artist,
                    year=t_year,
                    genre=t_genre,
                    duration=t_duration,
                    isrc=item.get("isrc"),
                    cover_url=t_cover,
                    audio_source_url=audio_url,
                    status=track_status,
                    error=error_msg,
                )

            tasks = [
                _resolve_single_track(item, idx)
                for idx, item in enumerate(raw_tracks, start=1)
            ]
            tracks = list(await asyncio.gather(*tasks))

            if tracks and all(t.status == "error" for t in tracks):
                raise AudioMatchError("Не удалось сопоставить ни один трек из альбома")

            album_meta = {
                "album": album_title,
                "album_artist": album_artist,
                "year": year_val,
                "genre": genre_name,
                "disc_total": disc_total_val,
                "track_total": total_tracks_count,
                "cover_url": cover_url,
            }
            return album_meta, tracks

        raise UnsupportedSourceError(f"Неподдерживаемый тип ресурса Deezer: {res_type}")

    async def _resolve_yandex(self, url: str) -> tuple[dict[str, Any], list[ResolvedTrack]]:
        """Разрешает трек, альбом или плейлист Яндекс Музыки."""
        res_type, params = await self._yandex.parse_url(url)

        if res_type == "track":
            track_id = params["track_id"]
            data = await self._yandex.get_track(track_id)
            title = data.get("title", "Unknown Track")
            artists = YandexMusicClient.format_artists(data.get("artists", []))
            dur_ms = data.get("durationMs") or 0
            duration = safe_int_duration(round(dur_ms / 1000.0))
            cover_url = YandexMusicClient.format_cover(data.get("ogImage") or data.get("coverUri"))

            albums = data.get("albums", [])
            album_info = albums[0] if albums else {}
            album_title = album_info.get("title", "")
            album_artist = YandexMusicClient.format_artists(album_info.get("artists", [])) or artists
            year = album_info.get("year")

            # Проверяем прямое скачивание с серверов Яндекса
            audio_url = await self._yandex.get_direct_download_url(track_id)
            if not audio_url:
                audio_url = await self._matcher.find_best_match(
                    title=title,
                    artist=artists.replace("\\\\", " "),
                    duration=duration,
                    cobalt_fallback_url=self._cobalt_api_url,
                )

            track = ResolvedTrack(
                track_id=str(data.get("id", track_id)),
                track_number=1,
                title=title,
                artist=artists,
                album=album_title,
                album_artist=album_artist,
                year=year,
                duration=duration,
                cover_url=cover_url,
                audio_source_url=audio_url,
            )
            album_meta = {
                "album": album_title,
                "album_artist": album_artist,
                "year": year,
                "cover_url": cover_url,
            }
            return album_meta, [track]

        if res_type in ("album", "playlist"):
            if res_type == "album":
                data = await self._yandex.get_album(params["album_id"])
                album_title = data.get("title", "Unknown Album")
                album_artist = YandexMusicClient.format_artists(data.get("artists", []))
                year_val = data.get("year")
                cover_url = YandexMusicClient.format_cover(data.get("ogImage") or data.get("coverUri"))
                raw_tracks: list[dict[str, Any]] = []
                for vol in data.get("volumes", []):
                    if isinstance(vol, list):
                        raw_tracks.extend(vol)
            else:
                data = await self._yandex.get_playlist(params.get("owner", ""), params.get("kind", ""))
                album_title = data.get("title", "Unknown Playlist")
                owner_info = data.get("owner", {})
                album_artist = owner_info.get("name") or owner_info.get("login") or "Yandex Music"
                year_val = None
                cover_url = YandexMusicClient.format_cover(data.get("ogImage") or data.get("cover", {}).get("uri"))
                raw_tracks = data.get("tracks", [])

            semaphore = asyncio.Semaphore(self._settings.MATCHER_CONCURRENCY)

            async def _resolve_single_yandex_track(item: dict[str, Any], default_pos: int) -> ResolvedTrack:
                t_data = item.get("track", item) if isinstance(item, dict) else {}
                t_title = t_data.get("title", "Unknown Track")
                t_artists = YandexMusicClient.format_artists(t_data.get("artists", []))
                dur_ms = t_data.get("durationMs") or 0
                t_duration = safe_int_duration(round(dur_ms / 1000.0))
                t_id = str(t_data.get("id", default_pos))

                t_albums = t_data.get("albums", [])
                t_album_info = t_albums[0] if t_albums else {}
                t_album = t_album_info.get("title", album_title)
                t_year = t_album_info.get("year", year_val)
                t_cover = YandexMusicClient.format_cover(t_data.get("ogImage") or t_data.get("coverUri")) or cover_url

                audio_url = ""
                track_status = "pending"
                error_msg: str | None = None

                try:
                    # Проверяем прямой URL
                    audio_url = await self._yandex.get_direct_download_url(t_id)
                    if not audio_url:
                        async with semaphore:
                            audio_url = await self._matcher.find_best_match(
                                title=t_title,
                                artist=t_artists.replace("\\\\", " "),
                                duration=t_duration,
                                cobalt_fallback_url=self._cobalt_api_url,
                            )
                except Exception as exc:  # noqa: BLE001
                    logger.warning("Не удалось сопоставить трек Яндекс Музыки '%s - %s': %s", t_artists, t_title, exc)
                    track_status = "error"
                    error_msg = str(exc)
                    audio_url = ""

                return ResolvedTrack(
                    track_id=t_id,
                    track_number=default_pos,
                    title=t_title,
                    artist=t_artists,
                    album=t_album,
                    album_artist=album_artist,
                    year=t_year,
                    duration=t_duration,
                    cover_url=t_cover,
                    audio_source_url=audio_url,
                    status=track_status,
                    error=error_msg,
                )

            tasks = [
                _resolve_single_yandex_track(item, idx)
                for idx, item in enumerate(raw_tracks, start=1)
            ]
            tracks = list(await asyncio.gather(*tasks))

            if tracks and all(t.status == "error" for t in tracks):
                raise AudioMatchError("Не удалось сопоставить ни один трек из коллекции Яндекс Музыки")

            album_meta = {
                "album": album_title,
                "album_artist": album_artist,
                "year": year_val,
                "cover_url": cover_url,
            }
            return album_meta, tracks

        raise UnsupportedSourceError(f"Неподдерживаемый тип ресурса Яндекс Музыки: {res_type}")

    async def _resolve_spotify(self, url: str) -> tuple[dict[str, Any], list[ResolvedTrack]]:
        """Разрешает трек, альбом или плейлист Spotify."""
        res_type, res_id = await self._spotify.parse_url(url)

        if res_type == "track":
            data = await self._spotify.get_track(res_id)
            title = data.get("title", "Unknown Track")
            artist = data.get("artist", "Unknown Artist")
            duration = safe_int_duration(data.get("duration"))
            cover_url = data.get("cover_url")

            audio_url = await self._matcher.find_best_match(
                title=title,
                artist=artist.replace("\\\\", " "),
                duration=duration,
                cobalt_fallback_url=self._cobalt_api_url,
            )

            track = ResolvedTrack(
                track_id=res_id,
                track_number=1,
                title=title,
                artist=artist,
                album=data.get("album", ""),
                album_artist=data.get("album_artist", artist),
                year=data.get("year"),
                duration=duration,
                isrc=data.get("isrc"),
                cover_url=cover_url,
                audio_source_url=audio_url,
            )
            album_meta = {
                "album": track.album,
                "album_artist": track.album_artist,
                "year": track.year,
                "cover_url": cover_url,
            }
            return album_meta, [track]

        if res_type in ("album", "playlist"):
            data = (
                await self._spotify.get_album(res_id)
                if res_type == "album"
                else await self._spotify.get_playlist(res_id)
            )

            album_title = data.get("title", "Unknown Collection")
            album_artist = data.get("artist", "Spotify")
            year_val = data.get("year")
            cover_url = data.get("cover_url")
            raw_tracks = data.get("tracks", [])

            semaphore = asyncio.Semaphore(self._settings.MATCHER_CONCURRENCY)

            async def _resolve_single_spotify_track(item: dict[str, Any], default_pos: int) -> ResolvedTrack:
                t_title = item.get("title", "Unknown Track")
                t_artists = item.get("artist", album_artist)
                t_duration = safe_int_duration(item.get("duration"))

                audio_url = ""
                track_status = "pending"
                error_msg: str | None = None

                try:
                    async with semaphore:
                        audio_url = await self._matcher.find_best_match(
                            title=t_title,
                            artist=t_artists.replace("\\\\", " "),
                            duration=t_duration,
                            cobalt_fallback_url=self._cobalt_api_url,
                        )
                except Exception as exc:  # noqa: BLE001
                    logger.warning("Не удалось сопоставить трек Spotify '%s - %s': %s", t_artists, t_title, exc)
                    track_status = "error"
                    error_msg = str(exc)
                    audio_url = ""

                return ResolvedTrack(
                    track_id=str(item.get("id", default_pos)),
                    track_number=int(item.get("track_number") or default_pos),
                    title=t_title,
                    artist=t_artists,
                    album=item.get("album", album_title),
                    album_artist=item.get("album_artist", album_artist),
                    year=item.get("year", year_val),
                    duration=t_duration,
                    isrc=item.get("isrc"),
                    cover_url=cover_url,
                    audio_source_url=audio_url,
                    status=track_status,
                    error=error_msg,
                )

            tasks = [
                _resolve_single_spotify_track(item, idx)
                for idx, item in enumerate(raw_tracks, start=1)
            ]
            tracks = list(await asyncio.gather(*tasks))

            if tracks and all(t.status == "error" for t in tracks):
                raise AudioMatchError("Не удалось сопоставить ни один трек из коллекции Spotify")

            album_meta = {
                "album": album_title,
                "album_artist": album_artist,
                "year": year_val,
                "cover_url": cover_url,
            }
            return album_meta, tracks

        raise UnsupportedSourceError(f"Неподдерживаемый тип ресурса Spotify: {res_type}")

    async def _resolve_apple_music(self, url: str) -> tuple[dict[str, Any], list[ResolvedTrack]]:
        """Разрешает трек, альбом или плейлист Apple Music."""
        res_type, params = self._apple.parse_url(url)

        if res_type == "track":
            track_id = params["track_id"]
            data = await self._apple.get_track(url, track_id)
            title = data.get("title", "Unknown Track")
            artist = data.get("artist", "Unknown Artist")
            duration = safe_int_duration(data.get("duration"))
            cover_url = data.get("cover_url")

            audio_url = await self._matcher.find_best_match(
                title=title,
                artist=artist.replace("\\\\", " "),
                duration=duration,
                cobalt_fallback_url=self._cobalt_api_url,
            )

            track = ResolvedTrack(
                track_id=track_id,
                track_number=1,
                title=title,
                artist=artist,
                album=data.get("album", ""),
                album_artist=data.get("album_artist", artist),
                year=data.get("year"),
                duration=duration,
                isrc=data.get("isrc"),
                cover_url=cover_url,
                audio_source_url=audio_url,
            )
            album_meta = {
                "album": track.album,
                "album_artist": track.album_artist,
                "year": track.year,
                "cover_url": cover_url,
            }
            return album_meta, [track]

        if res_type in ("album", "playlist"):
            data = await self._apple.get_collection(url)
            album_title = data.get("title", "Unknown Collection")
            album_artist = data.get("artist", "Apple Music")
            year_val = data.get("year")
            cover_url = data.get("cover_url")
            raw_tracks = data.get("tracks", [])

            semaphore = asyncio.Semaphore(self._settings.MATCHER_CONCURRENCY)

            async def _resolve_single_apple_track(item: dict[str, Any], default_pos: int) -> ResolvedTrack:
                t_title = item.get("title", "Unknown Track")
                t_artists = item.get("artist", album_artist)
                t_duration = safe_int_duration(item.get("duration"))
                t_cover = item.get("cover_url") or cover_url

                audio_url = ""
                track_status = "pending"
                error_msg: str | None = None

                try:
                    async with semaphore:
                        audio_url = await self._matcher.find_best_match(
                            title=t_title,
                            artist=t_artists.replace("\\\\", " "),
                            duration=t_duration,
                            cobalt_fallback_url=self._cobalt_api_url,
                        )
                except Exception as exc:  # noqa: BLE001
                    logger.warning("Не удалось сопоставить трек Apple Music '%s - %s': %s", t_artists, t_title, exc)
                    track_status = "error"
                    error_msg = str(exc)
                    audio_url = ""

                return ResolvedTrack(
                    track_id=str(item.get("id", default_pos)),
                    track_number=int(item.get("track_number") or default_pos),
                    title=t_title,
                    artist=t_artists,
                    album=item.get("album", album_title),
                    album_artist=item.get("album_artist", album_artist),
                    year=item.get("year", year_val),
                    duration=t_duration,
                    isrc=item.get("isrc"),
                    cover_url=t_cover,
                    audio_source_url=audio_url,
                    status=track_status,
                    error=error_msg,
                )

            tasks = [
                _resolve_single_apple_track(item, idx)
                for idx, item in enumerate(raw_tracks, start=1)
            ]
            tracks = list(await asyncio.gather(*tasks))

            if tracks and all(t.status == "error" for t in tracks):
                raise AudioMatchError("Не удалось сопоставить ни один трек из коллекции Apple Music")

            album_meta = {
                "album": album_title,
                "album_artist": album_artist,
                "year": year_val,
                "cover_url": cover_url,
            }
            return album_meta, tracks

        raise UnsupportedSourceError(f"Неподдерживаемый тип ресурса Apple Music: {res_type}")

    async def _resolve_ytdlp(
        self, url: str, source: str
    ) -> tuple[dict[str, Any], list[ResolvedTrack]]:
        """Разрешает метаданные YouTube/SoundCloud через yt-dlp."""
        ytdl_opts: dict[str, Any] = {
            "extract_flat": "in_playlist",
            "skip_download": True,
            "quiet": True,
            "no_warnings": True,
            "ignoreconfig": True,
            "http_headers": {
                "Accept-Language": "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7",
            },
            "extractor_args": {
                "youtube": {
                    "lang": ["ru"],
                }
            },
        }
        if self._cookies_path and self._cookies_path.is_file():
            ytdl_opts["cookiefile"] = str(self._cookies_path)

        def _extract(opts: dict[str, Any]) -> dict[str, Any]:
            with yt_dlp.YoutubeDL(opts) as ydl:
                return ydl.extract_info(url, download=False) or {}

        try:
            info = await asyncio.to_thread(_extract, ytdl_opts)
        except Exception as exc:
            # Если запрос с cookies упал (например, 'The page needs to be reloaded'), пробуем без cookies
            if "cookiefile" in ytdl_opts:
                logger.debug("Сбой yt-dlp с cookies (%s), повторная попытка без cookies...", exc)
                opts_clean = {k: v for k, v in ytdl_opts.items() if k != "cookiefile"}
                try:
                    info = await asyncio.to_thread(_extract, opts_clean)
                except Exception:  # noqa: BLE001
                    raise exc
            else:
                raise
        entries = info.get("entries")
        raw_entries = list(entries) if entries else []

        if raw_entries:
            # Плейлист или альбом
            album_title = (info.get("title") or "").strip() or (
                "SoundCloud Playlist" if source == "soundcloud" else "YouTube Playlist"
            )
            album_artist = (
                (info.get("uploader") or "").strip()
                or (info.get("artist") or "").strip()
                or (info.get("channel") or "").strip()
                or "Various Artists"
            )
            cover_url = info.get("thumbnail")
            if source == "soundcloud" and cover_url:
                cover_url = normalize_soundcloud_artwork(cover_url)

            # Если обложка альбома отсутствует, ищем первую доступную среди треков
            if not cover_url:
                for entry in raw_entries:
                    if entry and isinstance(entry, dict) and entry.get("thumbnail"):
                        cand = entry.get("thumbnail")
                        cover_url = (
                            normalize_soundcloud_artwork(cand)
                            if source == "soundcloud"
                            else cand
                        )
                        if cover_url:
                            break

            tracks: list[ResolvedTrack] = []
            for idx, entry in enumerate(raw_entries, start=1):
                if not entry or not isinstance(entry, dict):
                    continue
                t_title = (entry.get("title") or "").strip()
                cand_artist = (
                    (entry.get("artist") or "").strip()
                    or (entry.get("uploader") or "").strip()
                    or (entry.get("creator") or "").strip()
                )
                has_own_artist = bool(cand_artist)
                t_artist = cand_artist or album_artist
                t_duration = safe_int_duration(entry.get("duration"))

                t_cover = entry.get("thumbnail")
                if source == "soundcloud" and t_cover:
                    t_cover = normalize_soundcloud_artwork(t_cover)
                t_cover = t_cover or cover_url

                t_url = entry.get("url") or entry.get("webpage_url")
                if not t_url:
                    if source == "soundcloud":
                        t_url = (
                            f"https://api.soundcloud.com/tracks/{entry.get('id')}"
                            if entry.get("id")
                            else url
                        )
                    else:
                        t_url = (
                            f"https://www.youtube.com/watch?v={entry.get('id')}"
                            if entry.get("id")
                            else url
                        )

                # Обработка исполнителя и названия трека для SoundCloud
                if source == "soundcloud":
                    # 1. Извлечение артиста из названия вида 'Artist - Title'
                    if t_title:
                        title_parts = re.split(r"\s+[-–—]\s+", t_title, maxsplit=1)
                        if (
                            len(title_parts) == 2
                            and title_parts[0].strip()
                            and not title_parts[0].strip().isdigit()
                        ):
                            prefix_art = title_parts[0].strip()
                            rest_tit = title_parts[1].strip()
                            if (
                                not (entry.get("artist") or "").strip()
                                or cand_artist == album_artist
                                or not has_own_artist
                                or t_artist == album_artist
                                or t_artist.lower() == prefix_art.lower()
                                or prefix_art.lower().startswith(t_artist.lower())
                            ):
                                t_artist = prefix_art
                                t_title = rest_tit
                                has_own_artist = True

                    # 2. Fallback для SoundCloud по URL slug, если title или artist не были надежно извлечены
                    if t_url and "soundcloud.com" in t_url:
                        parsed_t = urllib.parse.urlparse(t_url)
                        hostname = (parsed_t.hostname or "").lower()
                        if hostname in ("soundcloud.com", "www.soundcloud.com", "m.soundcloud.com"):
                            parts = [p for p in parsed_t.path.strip("/").split("/") if p]
                            reserved = {
                                "tracks",
                                "sets",
                                "discover",
                                "stream",
                                "you",
                                "stations",
                                "upload",
                                "search",
                            }
                            if parts and parts[0].lower() not in reserved:
                                content_parts = [
                                    p for p in parts if not p.startswith("s-") and not p.isdigit()
                                ]
                                if len(content_parts) >= 2:
                                    if not has_own_artist or t_artist == album_artist:
                                        t_artist = content_parts[0].replace("-", " ").title()
                                    if not t_title or t_title.lower() in (
                                        f"track {idx}",
                                        f"track {idx:02d}",
                                        f"track_{idx}",
                                        "track",
                                    ):
                                        t_title = content_parts[1].replace("-", " ").title()
                                elif len(content_parts) == 1 and (not has_own_artist or t_artist == album_artist):
                                    t_artist = content_parts[0].replace("-", " ").title()

                if not t_title:
                    t_title = f"Track {idx}"

                tracks.append(
                    ResolvedTrack(
                        track_id=str(entry.get("id") or idx),
                        track_number=idx,
                        title=t_title,
                        artist=t_artist,
                        album=album_title,
                        album_artist=album_artist,
                        duration=t_duration,
                        cover_url=t_cover,
                        audio_source_url=t_url,
                    )
                )

            # Гарантируем, что cover_url альбома заполнен, если хотя бы у одного трека есть обложка
            if not cover_url and tracks:
                cover_url = next((t.cover_url for t in tracks if t.cover_url), None)

            album_meta = {
                "album": album_title,
                "album_artist": album_artist,
                "year": None,
                "cover_url": cover_url,
            }
            return album_meta, tracks

        # Одиночное видео / трек
        title = (info.get("title") or "").strip() or "Unknown Track"
        artist = (
            (info.get("artist") or "").strip()
            or (info.get("uploader") or "").strip()
            or (info.get("creator") or "").strip()
            or (info.get("channel") or "").strip()
            or "Unknown Artist"
        )
        if source == "soundcloud" and title:
            title_parts = re.split(r"\s+[-–—]\s+", title, maxsplit=1)
            if len(title_parts) == 2 and title_parts[0].strip() and not title_parts[0].strip().isdigit():
                prefix_art = title_parts[0].strip()
                rest_tit = title_parts[1].strip()
                if (
                    not (info.get("artist") or "").strip()
                    or artist.lower() == prefix_art.lower()
                    or prefix_art.lower().startswith(artist.lower())
                ):
                    artist = prefix_art
                    title = rest_tit
        duration = safe_int_duration(info.get("duration"))
        cover_url = info.get("thumbnail")
        if source == "soundcloud" and cover_url:
            cover_url = normalize_soundcloud_artwork(cover_url)
        track_url = info.get("webpage_url") or info.get("url") or url

        album = (info.get("album") or "").strip()
        album_artist = (info.get("album_artist") or "").strip() or artist

        track = ResolvedTrack(
            track_id=str(info.get("id") or "1"),
            track_number=1,
            title=title,
            artist=artist,
            album=album,
            album_artist=album_artist,
            duration=duration,
            cover_url=cover_url,
            audio_source_url=track_url,
        )

        album_meta = {
            "album": album,
            "album_artist": album_artist,
            "year": None,
            "cover_url": cover_url,
        }
        return album_meta, [track]
