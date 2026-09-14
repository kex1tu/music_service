"""Сервис глобального поиска и аудита медиатеки (Library Audit Service).

Обеспечивает:
1. In-memory индексирование коллекции (Zero-Database) с инкрементальным кэшированием по st_mtime.
2. Мгновенный глобальный поиск (Search) по исполнителям, альбомам и трекам.
3. Аудит целостности медиатеки:
   - Обнаружение треков без синхронизированных или текстовых слов (.lrc/.txt).
   - Пакетный фоновый сбор недостающих текстов через LRCLIB и Genius.
   - Обнаружение релизов и треков без графических обложек.
   - Интеллектуальный поиск дубликатов песен по нормализованным метаданным и длительности.
"""

import asyncio
import logging
import os
import re
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from app.core.sandbox import PathSandbox
from app.schemas.library_audit import (
    AuditSummaryResponse,
    BatchLyricsProgressResponse,
    DuplicateGroupItem,
    DuplicatesResponse,
    DuplicateTrackItem,
    LibrarySearchResponse,
    MissingCoverItem,
    MissingLyricsItem,
    SearchAlbumItem,
    SearchArtistItem,
    SearchTrackItem,
)
from app.schemas.tag import TrackTags
from app.services.lyrics_service import LyricsService
from app.services.tag_service import TagService

logger = logging.getLogger("music_manager.services.library_audit")


def normalize_clean_string(val: str) -> str:
    """Очищает строку для сравнения (удаляет пунктуацию, регистр, соисполнителей и лишние пробелы)."""
    if not val:
        return ""
    s = val.lower()
    # Удаляем feat/ft блоки в скобках и без
    s = re.sub(r"[\(\[\{]\s*(?:feat|ft|featuring)\b[^\)\]\}]*[\)\]\}]", "", s, flags=re.IGNORECASE)
    s = re.sub(r"\b(?:feat|ft|featuring)\b.*$", "", s, flags=re.IGNORECASE)
    # Заменяем разделители на пробелы
    s = re.sub(r"[_\-–—/\\|:]", " ", s)
    # Удаляем всю пунктуацию, кроме букв и цифр
    s = re.sub(r"[^\w\s]", "", s, flags=re.UNICODE)
    return " ".join(s.split()).strip()


class LibraryAuditService:
    """Сервис аудита и поиска по файловой медиатеке MUSIC_DIR."""

    def __init__(
        self,
        music_sandbox: PathSandbox,
        tag_service: TagService,
        lyrics_service: LyricsService | None = None,
    ) -> None:
        self._music_sandbox = music_sandbox
        self._tag_service = tag_service
        self._lyrics_service = lyrics_service
        self._lock = asyncio.Lock()
        # Кеш треков: rel_path -> dict(mtime=float, size=int, track=TrackTags)
        self._cache: dict[str, dict[str, Any]] = {}
        # Прогресс пакетного сбора текстов
        self._lyrics_progress = BatchLyricsProgressResponse(is_running=False)
        self._lyrics_task: asyncio.Task | None = None

    def invalidate(self) -> None:
        """Сбрасывает кеш индекса."""
        self._cache.clear()

    async def ensure_index(self, force: bool = False) -> None:
        """Выполняет инкрементальное обновление in-memory индекса медиатеки."""
        async with self._lock:
            if force:
                self._cache.clear()

            music_root = self._music_sandbox.root
            if not music_root.exists() or not music_root.is_dir():
                self._cache.clear()
                return

            # Сканируем рекурсивно все MP3-файлы
            found_paths: set[str] = set()

            for root_dir, dirs, files in os.walk(music_root):
                # Игнорируем скрытые папки (.git, etc)
                dirs[:] = [d for d in dirs if not d.startswith(".")]

                for fname in files:
                    if fname.startswith(".") or not fname.lower().endswith(".mp3"):
                        continue

                    full_path = Path(root_dir) / fname
                    try:
                        rel_path = str(self._music_sandbox.to_relative(full_path)).replace("\\", "/")
                    except (ValueError, OSError) as exc:
                        logger.debug("Пропуск файла вне песочницы %s: %s", full_path, exc)
                        continue

                    found_paths.add(rel_path)

                    try:
                        st = full_path.stat()
                        mtime = st.st_mtime
                        size = st.st_size
                    except OSError:
                        continue

                    cached = self._cache.get(rel_path)
                    # Проверяем также существование внешних текстов, так как они могли добавиться/удалиться
                    lrc_exists = full_path.with_suffix(".lrc").exists()
                    txt_exists = full_path.with_suffix(".txt").exists()
                    cur_lyrics_status = "lrc" if lrc_exists else ("txt" if txt_exists else "none")

                    if (
                        cached
                        and cached["mtime"] == mtime
                        and cached["track"].lyrics_status == cur_lyrics_status
                    ):
                        continue

                    try:
                        track_tags = self._tag_service.read_tags(full_path, rel_path=rel_path)
                        track_tags.lyrics_status = cur_lyrics_status
                        self._cache[rel_path] = {
                            "mtime": mtime,
                            "size": size,
                            "track": track_tags,
                        }
                    except (OSError, ValueError, TypeError) as exc:
                        logger.warning("Ошибка чтения тегов трека %s: %s", rel_path, exc)

            # Удаляем из кеша файлы, которых больше нет на диске
            stale_keys = [k for k in self._cache if k not in found_paths]
            for k in stale_keys:
                self._cache.pop(k, None)

    async def search(self, query: str, limit: int = 20) -> LibrarySearchResponse:
        """Выполняет глобальный поиск по артистам, альбомам и трекам."""
        clean_q = query.strip()
        if not clean_q:
            return LibrarySearchResponse(query=query)

        await self.ensure_index()

        q_lower = clean_q.lower()

        matched_tracks: list[SearchTrackItem] = []
        artist_counts: dict[str, int] = {}
        artist_first_paths: dict[str, str] = {}
        albums_map: dict[str, dict[str, Any]] = {}

        for item in self._cache.values():
            t: TrackTags = item["track"]
            title_match = q_lower in t.title.lower() if t.title else False
            artist_match = q_lower in t.artist.lower() if t.artist else False
            album_match = q_lower in t.album.lower() if t.album else False

            # Сбор статистики по артистам
            if t.artist:
                for art in self._tag_service.parse_multivalue(t.artist):
                    art_name = art.strip()
                    if art_name:
                        artist_counts[art_name] = artist_counts.get(art_name, 0) + 1
                        if art_name not in artist_first_paths:
                            artist_first_paths[art_name] = t.path

            # Сбор информации об альбоме
            album_dir = str(Path(t.path).parent).replace("\\", "/")
            if album_dir == ".":
                album_dir = ""
            album_key = f"{t.album_artist or t.artist}::{t.album or album_dir}"

            if album_key not in albums_map:
                has_cover = t.has_cover
                if not has_cover and album_dir:
                    try:
                        dir_path = self._music_sandbox.resolve(album_dir, allow_root=True)
                        for c_name in ("cover.jpg", "cover.png", "cover.jpeg", "folder.jpg", "folder.png"):
                            if (dir_path / c_name).exists():
                                has_cover = True
                                break
                    except (OSError, ValueError) as exc:
                        logger.debug("Ошибка проверки обложки в %s: %s", album_dir, exc)

                albums_map[album_key] = {
                    "album": t.album or Path(t.path).parent.name or "Альбом без названия",
                    "album_artist": t.album_artist or t.artist or "Неизвестный исполнитель",
                    "year": t.year,
                    "path": album_dir,
                    "track_count": 0,
                    "has_cover": has_cover,
                }
            albums_map[album_key]["track_count"] += 1
            if t.has_cover:
                albums_map[album_key]["has_cover"] = True

            # Проверяем совпадение трека
            if title_match or artist_match or album_match:
                matched_tracks.append(
                    SearchTrackItem(
                        path=t.path,
                        title=t.title or Path(t.path).stem,
                        artist=t.artist or "Неизвестный исполнитель",
                        album=t.album or "",
                        track_number=t.track_number,
                        duration=t.duration,
                        has_cover=t.has_cover,
                        lyrics_status=t.lyrics_status,
                    )
                )

        # Фильтрация и форматирование артистов
        matched_artists: list[SearchArtistItem] = []
        for art_name, cnt in sorted(artist_counts.items(), key=lambda x: -x[1]):
            if q_lower in art_name.lower():
                matched_artists.append(
                    SearchArtistItem(
                        name=art_name,
                        track_count=cnt,
                        first_path=artist_first_paths.get(art_name, ""),
                    )
                )

        # Фильтрация альбомов
        matched_albums: list[SearchAlbumItem] = []
        for alb_data in albums_map.values():
            if (
                q_lower in alb_data["album"].lower()
                or q_lower in alb_data["album_artist"].lower()
            ):
                matched_albums.append(SearchAlbumItem(**alb_data))

        total_matches = len(matched_artists) + len(matched_albums) + len(matched_tracks)

        return LibrarySearchResponse(
            query=query,
            artists=matched_artists[:limit],
            albums=matched_albums[:limit],
            tracks=matched_tracks[:limit],
            total_matches=total_matches,
        )

    async def get_summary(self) -> AuditSummaryResponse:
        """Возвращает агрегированные метрики аудита медиатеки."""
        await self.ensure_index()

        total_tracks = len(self._cache)
        missing_lyrics_count = sum(
            1 for item in self._cache.values() if item["track"].lyrics_status == "none"
        )

        missing_covers = await self.get_missing_covers()
        missing_covers_count = len(missing_covers)

        duplicates = await self.find_duplicates()
        duplicates_count = duplicates.total_duplicate_files

        # Подсчет альбомов
        album_paths = {
            str(Path(item["track"].path).parent).replace("\\", "/")
            for item in self._cache.values()
        }
        album_paths.discard(".")
        album_paths.discard("")
        total_albums = len(album_paths)

        return AuditSummaryResponse(
            total_tracks=total_tracks,
            total_albums=total_albums,
            missing_lyrics_count=missing_lyrics_count,
            missing_covers_count=missing_covers_count,
            duplicates_count=duplicates_count,
        )

    async def get_missing_lyrics(self) -> list[MissingLyricsItem]:
        """Возвращает список треков без внешних текстов песен (.lrc/.txt)."""
        await self.ensure_index()

        missing: list[MissingLyricsItem] = []
        for item in self._cache.values():
            t: TrackTags = item["track"]
            if t.lyrics_status == "none":
                parent_dir = str(Path(t.path).parent).replace("\\", "/")
                directory = "" if parent_dir == "." else parent_dir
                missing.append(
                    MissingLyricsItem(
                        path=t.path,
                        title=t.title or Path(t.path).stem,
                        artist=t.artist or "Неизвестный исполнитель",
                        album=t.album or "",
                        album_artist=t.album_artist or "",
                        directory=directory,
                    )
                )

        return sorted(missing, key=lambda x: (x.artist.lower(), x.album.lower(), x.title.lower()))

    def start_batch_lyrics_fetch(self) -> BatchLyricsProgressResponse:
        """Запускает фоновый процесс автопоиска текстов для всех треков без слов."""
        if self._lyrics_progress.is_running:
            return self._lyrics_progress

        if not self._lyrics_service:
            return BatchLyricsProgressResponse(is_running=False)

        # Синхронно берем список из текущего кеша
        missing_tracks: list[MissingLyricsItem] = []
        for item in self._cache.values():
            if item["track"].lyrics_status == "none":
                t = item["track"]
                parent_dir = str(Path(t.path).parent).replace("\\", "/")
                directory = "" if parent_dir == "." else parent_dir
                missing_tracks.append(
                    MissingLyricsItem(
                        path=t.path,
                        title=t.title or Path(t.path).stem,
                        artist=t.artist or "",
                        album=t.album or "",
                        album_artist=t.album_artist or "",
                        directory=directory,
                    )
                )

        if not missing_tracks:
            return BatchLyricsProgressResponse(is_running=False, total=0)

        self._lyrics_progress = BatchLyricsProgressResponse(
            is_running=True,
            total=len(missing_tracks),
            current=0,
            found=0,
            failed=0,
            current_track="Инициализация поиска...",
        )

        self._lyrics_task = asyncio.create_task(self._run_batch_lyrics(missing_tracks))
        return self._lyrics_progress

    def get_batch_lyrics_progress(self) -> BatchLyricsProgressResponse:
        """Возвращает текущий прогресс фонового поиска текстов."""
        return self._lyrics_progress

    async def _run_batch_lyrics(self, tracks: list[MissingLyricsItem]) -> None:
        """Фоновая корутина постепенного поиска текстов с ограничением частоты запросов."""
        assert self._lyrics_service is not None

        for track in tracks:
            self._lyrics_progress.current_track = f"{track.artist} - {track.title}"
            try:
                # Поиск в LRCLIB / Genius
                res = await self._lyrics_service.search(
                    artist=track.artist,
                    title=track.title,
                    album=track.album or None,
                )

                if res and res.lyrics:
                    kind = "lrc" if res.synced else "txt"
                    self._lyrics_service.save_lyrics(
                        rel_mp3_path=track.path,
                        content=res.lyrics,
                        kind=kind,
                    )
                    if track.path in self._cache:
                        self._cache[track.path]["track"].lyrics_status = kind
                    self._lyrics_progress.found += 1
                else:
                    self._lyrics_progress.failed += 1
            except (OSError, ValueError, RuntimeError) as exc:
                logger.warning("Сбой поиска текста для %s: %s", track.path, exc)
                self._lyrics_progress.failed += 1

            self._lyrics_progress.current += 1
            # Задержка 250 мс для защиты от Rate Limiting (429)
            await asyncio.sleep(0.25)

        self._lyrics_progress.is_running = False
        self._lyrics_progress.current_track = "Поиск завершен"

    async def get_missing_covers(self) -> list[MissingCoverItem]:
        """Возвращает список папок альбомов или отдельных треков без обложек."""
        await self.ensure_index()

        missing: list[MissingCoverItem] = []
        # Группируем по альбомам
        albums_dict: dict[str, list[TrackTags]] = {}

        for item in self._cache.values():
            t: TrackTags = item["track"]
            album_dir = str(Path(t.path).parent).replace("\\", "/")
            if album_dir == ".":
                album_dir = ""
            albums_dict.setdefault(album_dir, []).append(t)

        for album_dir, tracks in albums_dict.items():
            has_cover = any(t.has_cover for t in tracks)

            if not has_cover and album_dir:
                try:
                    dir_path = self._music_sandbox.resolve(album_dir, allow_root=True)
                    for c_name in ("cover.jpg", "cover.png", "cover.jpeg", "folder.jpg", "folder.png"):
                        if (dir_path / c_name).exists():
                            has_cover = True
                            break
                except (OSError, ValueError) as exc:
                    logger.debug("Ошибка поиска файла обложки в %s: %s", album_dir, exc)

            if not has_cover:
                first_t = tracks[0]
                is_album = bool(album_dir)
                missing.append(
                    MissingCoverItem(
                        path=album_dir or first_t.path,
                        title=first_t.album or (Path(album_dir).name if album_dir else first_t.title),
                        artist=first_t.album_artist or first_t.artist or "Неизвестный исполнитель",
                        album=first_t.album or "",
                        is_album_dir=is_album,
                    )
                )

        return sorted(missing, key=lambda x: (x.artist.lower(), x.title.lower()))

    async def find_duplicates(self) -> DuplicatesResponse:
        """Находит дубликаты треков по нормализованным метаданным и схожести хронометража."""
        await self.ensure_index()

        # Группировка по композитному нормализованному ключу
        groups_raw: dict[str, list[dict[str, Any]]] = {}

        for rel_path, item in self._cache.items():
            t: TrackTags = item["track"]
            clean_art = normalize_clean_string(t.artist or t.album_artist or "")
            clean_tit = normalize_clean_string(t.title or Path(rel_path).stem)

            if not clean_tit:
                continue

            composite_key = f"{clean_art}::{clean_tit}" if clean_art else clean_tit
            groups_raw.setdefault(composite_key, []).append(
                {
                    "path": rel_path,
                    "filename": Path(rel_path).name,
                    "size": item.get("size"),
                    "duration": t.duration,
                    "mtime": item.get("mtime", 0),
                    "title": t.title or Path(rel_path).stem,
                    "artist": t.artist or "Неизвестный исполнитель",
                }
            )

        duplicate_groups: list[DuplicateGroupItem] = []
        total_dup_files = 0

        for key, raw_tracks in groups_raw.items():
            if len(raw_tracks) < 2:
                continue

            # Проверяем подтверждение схожести длительности (±2с) или размера
            verified_tracks: list[dict[str, Any]] = []

            for i, tr1 in enumerate(raw_tracks):
                is_match = False
                for j, tr2 in enumerate(raw_tracks):
                    if i == j:
                        continue

                    d1, d2 = tr1["duration"], tr2["duration"]
                    if d1 is not None and d2 is not None:
                        if abs(d1 - d2) <= 2:
                            is_match = True
                            break
                    elif tr1["size"] is not None and tr2["size"] is not None:
                        if tr1["size"] == tr2["size"]:
                            is_match = True
                            break
                    else:
                        # Если нет длительности, то совпадение нормализованных названия и автора уже является кандидатом
                        is_match = True
                        break

                if is_match and tr1 not in verified_tracks:
                    verified_tracks.append(tr1)

            if len(verified_tracks) >= 2:
                items = [
                    DuplicateTrackItem(
                        path=tr["path"],
                        filename=tr["filename"],
                        size=tr["size"],
                        duration=tr["duration"],
                        bitrate=320,
                        modified_at=datetime.fromtimestamp(tr["mtime"], tz=timezone.utc).strftime("%Y-%m-%d %H:%M"),
                    )
                    for tr in verified_tracks
                ]
                duplicate_groups.append(
                    DuplicateGroupItem(
                        key=key,
                        title=verified_tracks[0]["title"],
                        artist=verified_tracks[0]["artist"],
                        tracks=items,
                    )
                )
                total_dup_files += len(items) - 1

        return DuplicatesResponse(
            groups=duplicate_groups,
            total_duplicate_files=total_dup_files,
        )
