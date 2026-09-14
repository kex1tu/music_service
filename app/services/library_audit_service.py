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
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from app.core.exceptions import NotFoundError
from app.core.sandbox import PathSandbox
from app.schemas.library_audit import (
    AuditSummaryResponse,
    BatchLyricsProgressResponse,
    DuplicateGroupItem,
    DuplicatesResponse,
    DuplicateTrackItem,
    IncompleteAlbumItem,
    IncompleteAlbumsResponse,
    IncompleteTrackInfo,
    LibrarySearchResponse,
    MissingCoverItem,
    MissingLyricsItem,
    SearchAlbumItem,
    SearchArtistItem,
    SearchTrackItem,
)
from app.schemas.tag import TrackTags
from app.services.deezer_client import DeezerClient
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
        deezer_client: DeezerClient | None = None,
    ) -> None:
        self._music_sandbox = music_sandbox
        self._tag_service = tag_service
        self._lyrics_service = lyrics_service
        self._deezer_client = deezer_client
        self._lock = asyncio.Lock()
        # Кеш треков: rel_path -> dict(mtime=float, size=int, track=TrackTags)
        self._cache: dict[str, dict[str, Any]] = {}
        # Кеш сверок релизов с каталогом Deezer: path -> IncompleteAlbumItem
        self._catalog_cache: dict[str, IncompleteAlbumItem] = {}
        # Прогресс пакетного сбора текстов
        self._lyrics_progress = BatchLyricsProgressResponse(is_running=False)
        self._lyrics_task: asyncio.Task | None = None

    def invalidate(self) -> None:
        """Сбрасывает кеш индекса и каталога."""
        self._cache.clear()
        self._catalog_cache.clear()

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

        incomplete_res = await self.get_incomplete_albums()
        incomplete_albums_count = incomplete_res.total_incomplete_albums

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
            incomplete_albums_count=incomplete_albums_count,
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
                        track_count=len(tracks),
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
                        size_bytes=tr["size"],
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

    async def get_incomplete_albums(self) -> IncompleteAlbumsResponse:
        """Обнаруживает альбомы с недостающими треками на основе тегов TRCK/TPOS и непрерывности нумерации."""
        await self.ensure_index()

        albums_dict: dict[str, list[TrackTags]] = {}
        for item in self._cache.values():
            t: TrackTags = item["track"]
            p = Path(t.path).parent
            # Если трек лежит в поддиректории CD1, CD2, Disc 1 - группируем с родительским каталогом альбома
            if p.name.lower().startswith(("cd", "disc", "диск")) and str(p.parent) != ".":
                p = p.parent
            album_dir = str(p).replace("\\", "/")
            if album_dir == ".":
                album_dir = ""
            albums_dict.setdefault(album_dir, []).append(t)

        incomplete_albums: list[IncompleteAlbumItem] = []
        total_missing_tracks_count = 0

        for album_dir, tracks in albums_dict.items():
            if not tracks:
                continue

            first_t = tracks[0]
            album_name = next(
                (t.album for t in tracks if t.album),
                (Path(album_dir).name if album_dir else first_t.title) or "Альбом",
            )
            artist_name = next(
                (t.album_artist or t.artist for t in tracks if (t.album_artist or t.artist)),
                "Неизвестный исполнитель",
            )
            year = next((t.year for t in tracks if t.year), None)

            # Определение сингла: если 1 трек и в названии папки или альбома есть single / сингл
            alb_dir_name = Path(album_dir).name.lower() if album_dir else ""
            alb_name_lower = (album_name or "").lower()
            if len(tracks) == 1 and (
                "single" in alb_dir_name
                or "сингл" in alb_dir_name
                or "single" in alb_name_lower
                or "сингл" in alb_name_lower
            ):
                continue

            # 1. Проверяем кэш сверок с Deezer
            if album_dir in self._catalog_cache:
                cached_match = self._catalog_cache[album_dir]
                # Если сверенный релиз укомплектован (все треки присутствуют) -> пропускаем!
                if cached_match.expected_count <= cached_match.present_count or not cached_match.missing_tracks:
                    continue
                # Иначе включаем сверенный результат из кеша
                incomplete_albums.append(cached_match)
                total_missing_tracks_count += len(cached_match.missing_tracks)
                continue

            # Проверка наличия обложки
            has_cover = any(t.has_cover for t in tracks)
            if not has_cover and album_dir:
                try:
                    dir_path = self._music_sandbox.resolve(album_dir, allow_root=True)
                    for c_name in ("cover.jpg", "cover.png", "cover.jpeg", "folder.jpg", "folder.png"):
                        if (dir_path / c_name).exists():
                            has_cover = True
                            break
                except (OSError, ValueError):
                    pass

            # Группируем треки по номерам дисков (disc_number)
            discs: dict[int, list[TrackTags]] = {}
            for t in tracks:
                d_num = t.disc_number if t.disc_number and t.disc_number > 0 else 1
                discs.setdefault(d_num, []).append(t)

            disc_totals = [t.disc_total for t in tracks if t.disc_total and t.disc_total > 0]
            max_disc_total = max(disc_totals) if disc_totals else max(discs.keys())

            is_incomplete = False
            missing_tracks: list[IncompleteTrackInfo] = []
            missing_numbers: list[int] = []
            all_tracks: list[IncompleteTrackInfo] = []
            total_expected_tracks = 0
            total_present_tracks = len(tracks)

            # Проверяем каждый диск релиза
            for disc_num in sorted(range(1, max(max_disc_total, max(discs.keys())) + 1)):
                disc_tracks = discs.get(disc_num, [])
                track_map: dict[int, TrackTags] = {}
                for t in disc_tracks:
                    if t.track_number and t.track_number > 0:
                        track_map[t.track_number] = t

                track_totals = [t.track_total for t in disc_tracks if t.track_total and t.track_total > 0]
                if track_totals:
                    counts = Counter(track_totals)
                    consensus_total, freq = counts.most_common(1)[0]
                    # Если за значение проголосовало большинство или оно совпадает с числом треков
                    if freq >= len(disc_tracks) / 2 or consensus_total == len(disc_tracks):
                        max_track_total = consensus_total
                    else:
                        max_track_total = max(track_totals)
                else:
                    max_track_total = None

                # Если это одиночный трек в папке сингла, не считаем его частью сборника на 35 треков
                if len(disc_tracks) == 1:
                    alb_lower = (album_name or "").lower()
                    if "single" in alb_lower or "сингл" in alb_lower:
                        max_track_total = 1

                max_track_num = max(track_map.keys()) if track_map else 0

                # Определение ожидаемого количества треков:
                # 1) Если задан track_total в тегах TRCK -> используем его.
                # 2) Иначе, если max_track_num > len(track_map) и (len(track_map) >= 2 or max_track_num > 1) -> разрыв.
                # 3) Иначе диск считается цельным.
                if max_track_total is not None and max_track_total > len(track_map):
                    disc_expected = max_track_total
                elif max_track_num > len(track_map) and (len(track_map) >= 2 or max_track_num > 1):
                    disc_expected = max_track_num
                else:
                    disc_expected = len(track_map) if track_map else len(disc_tracks)

                total_expected_tracks += disc_expected

                for pos in range(1, disc_expected + 1):
                    if pos in track_map:
                        loc_t = track_map[pos]
                        all_tracks.append(
                            IncompleteTrackInfo(
                                track_number=pos,
                                disc_number=disc_num,
                                title=loc_t.title or Path(loc_t.path).stem,
                                duration=loc_t.duration,
                                is_present=True,
                                local_path=loc_t.path,
                            )
                        )
                    else:
                        is_incomplete = True
                        missing_numbers.append(pos)
                        missing_trk = IncompleteTrackInfo(
                            track_number=pos,
                            disc_number=disc_num,
                            title=f"Трек {pos}",
                            is_present=False,
                            local_path=None,
                        )
                        missing_tracks.append(missing_trk)
                        all_tracks.append(missing_trk)

            if is_incomplete and total_expected_tracks > total_present_tracks:
                # Кандидат на неполный альбом.
                # Попробуем автоматически сверить с Deezer, если клиент доступен
                if self._deezer_client:
                    try:
                        matched = await self.match_album_catalog(album_dir or first_t.path)
                        if matched.expected_count <= matched.present_count or not matched.missing_tracks:
                            # В официальном каталоге Deezer все треки релиза на месте (ложный признак неполноты по тегам)
                            continue
                        incomplete_albums.append(matched)
                        total_missing_tracks_count += len(matched.missing_tracks)
                        continue
                    except Exception as exc:  # noqa: BLE001
                        logger.debug("Сверка с каталогом Deezer для %s завершилась с ошибкой: %s", album_dir, exc)

                incomplete_albums.append(
                    IncompleteAlbumItem(
                        path=album_dir or first_t.path,
                        album=album_name,
                        artist=artist_name,
                        year=year,
                        has_cover=has_cover,
                        present_count=total_present_tracks,
                        expected_count=total_expected_tracks,
                        missing_track_numbers=missing_numbers,
                        missing_tracks=missing_tracks,
                        all_tracks=all_tracks,
                        is_catalog_matched=False,
                    )
                )
                total_missing_tracks_count += len(missing_tracks)

        incomplete_albums.sort(key=lambda x: (x.artist.lower(), x.album.lower()))

        return IncompleteAlbumsResponse(
            albums=incomplete_albums,
            total_incomplete_albums=len(incomplete_albums),
            total_missing_tracks=total_missing_tracks_count,
        )

    async def match_album_catalog(
        self,
        album_path: str,
        deezer_album_id: str | None = None,
    ) -> IncompleteAlbumItem:
        """Сверяет состав треков альбома с официальным каталогом Deezer."""
        await self.ensure_index()

        dir_path = self._music_sandbox.resolve(album_path, allow_root=True)
        if not dir_path.exists():
            raise NotFoundError(f"Каталог или трек не найден: {album_path}")

        # Нормализуем относительный путь
        norm_album_path = str(self._music_sandbox.to_relative(dir_path)).replace("\\", "/")
        if norm_album_path == ".":
            norm_album_path = ""

        local_tracks: list[TrackTags] = []
        for item in self._cache.values():
            t: TrackTags = item["track"]
            t_dir = str(Path(t.path).parent).replace("\\", "/")
            if t_dir == ".":
                t_dir = ""
            if t_dir == norm_album_path or t_dir.startswith(f"{norm_album_path}/"):
                local_tracks.append(t)

        if not local_tracks and dir_path.is_dir():
            for f in dir_path.glob("*.mp3"):
                try:
                    rel = str(self._music_sandbox.to_relative(f)).replace("\\", "/")
                    local_tracks.append(self._tag_service.read_tags(f, rel_path=rel))
                except (OSError, ValueError, TypeError) as exc:
                    logger.debug("Ошибка чтения тегов при сканировании каталога %s: %s", f, exc)

        album_name = next(
            (t.album for t in local_tracks if t.album),
            dir_path.name if norm_album_path else "Альбом",
        )
        artist_name = next(
            (t.album_artist or t.artist for t in local_tracks if (t.album_artist or t.artist)),
            "",
        )
        year = next((t.year for t in local_tracks if t.year), None)
        has_cover = any(t.has_cover for t in local_tracks)

        # Если клиента Deezer нет — возвращаем локальный расчет
        if not self._deezer_client:
            offline_res = await self.get_incomplete_albums()
            for alb in offline_res.albums:
                if alb.path == norm_album_path:
                    return alb
            return IncompleteAlbumItem(
                path=norm_album_path,
                album=album_name,
                artist=artist_name,
                year=year,
                has_cover=has_cover,
                present_count=len(local_tracks),
                expected_count=len(local_tracks),
            )

        album_data: dict[str, Any] | None = None
        if deezer_album_id:
            try:
                album_data = await self._deezer_client.get_album(deezer_album_id)
            except Exception as exc:  # noqa: BLE001
                logger.warning("Не удалось получить альбом Deezer по ID %s: %s", deezer_album_id, exc)

        if not album_data:
            clean_alb = re.sub(
                r"\s*[-–—]\s*(?:Single|EP|LP)(?:\s*[-–—]\s*\d{4})?",
                "",
                album_name,
                flags=re.IGNORECASE,
            )
            clean_alb = re.sub(
                r"\s*[\(\[]\s*(?:feat\.?|ft\.?|featuring|with)\s+[^\]\)]+?[\)\]]",
                "",
                clean_alb,
                flags=re.IGNORECASE,
            )
            clean_alb = re.sub(
                r"\s*[\(\[](?:Expanded|Deluxe|Special|Bonus|Extended|Remastered|Edition|Version|Anniversary|Original).*?[\)\]]",
                "",
                clean_alb,
                flags=re.IGNORECASE,
            ).strip()

            queries = [f"{artist_name} {album_name}".strip()]
            if clean_alb and clean_alb.lower() != album_name.lower():
                queries.append(f"{artist_name} {clean_alb}".strip())
                queries.append(clean_alb)

            for q in queries:
                if not q:
                    continue
                try:
                    candidates = await self._deezer_client.search_albums(q, limit=5)
                    if candidates:
                        best_candidate = candidates[0]
                        clean_target_alb = normalize_clean_string(clean_alb or album_name)
                        clean_target_art = normalize_clean_string(artist_name)

                        for cand in candidates:
                            c_title = normalize_clean_string(cand.get("title", ""))
                            c_art = normalize_clean_string(cand.get("artist", {}).get("name", ""))
                            if c_title == clean_target_alb and (
                                not clean_target_art
                                or clean_target_art in c_art
                                or c_art in clean_target_art
                            ):
                                best_candidate = cand
                                break

                        cand_id = str(best_candidate.get("id"))
                        album_data = await self._deezer_client.get_album(cand_id)
                        if album_data:
                            break
                except Exception as exc:  # noqa: BLE001
                    logger.warning("Сбой поиска альбома в Deezer (%s): %s", q, exc)

        if not album_data:
            fallback_item = IncompleteAlbumItem(
                path=norm_album_path,
                album=album_name,
                artist=artist_name,
                year=year,
                has_cover=has_cover,
                present_count=len(local_tracks),
                expected_count=len(local_tracks),
                is_catalog_matched=False,
            )
            return fallback_item

        deezer_tracks = album_data.get("tracks", {}).get("data", [])
        cover_url = DeezerClient.extract_best_cover_url(album_data)
        deezer_artist = album_data.get("artist", {}).get("name", artist_name)
        deezer_title = album_data.get("title", album_name)
        deezer_id = str(album_data.get("id", ""))

        all_matched_tracks: list[IncompleteTrackInfo] = []
        missing_tracks: list[IncompleteTrackInfo] = []
        missing_numbers: list[int] = []
        present_count = 0
        used_local_indices: set[int] = set()

        for idx, dz_t in enumerate(deezer_tracks, start=1):
            dz_pos = int(dz_t.get("track_position") or idx)
            dz_disc = int(dz_t.get("disk_number") or 1)
            dz_title = dz_t.get("title", "")
            dz_dur = dz_t.get("duration")
            dz_tid = str(dz_t.get("id", ""))
            dz_url = f"https://www.deezer.com/track/{dz_tid}" if dz_tid else None

            clean_dz_title = normalize_clean_string(dz_title)
            matched_loc: TrackTags | None = None
            matched_loc_idx: int | None = None

            # 1. Попытка точного сопоставления по (disc_number, track_number)
            for loc_i, loc in enumerate(local_tracks):
                if loc_i in used_local_indices:
                    continue
                loc_disc = loc.disc_number if loc.disc_number and loc.disc_number > 0 else 1
                if loc_disc == dz_disc and loc.track_number == dz_pos:
                    matched_loc = loc
                    matched_loc_idx = loc_i
                    break

            # 2. Попытка сопоставления только по track_number (если номер диска не прописан)
            if not matched_loc:
                for loc_i, loc in enumerate(local_tracks):
                    if loc_i in used_local_indices:
                        continue
                    if loc.track_number == dz_pos:
                        matched_loc = loc
                        matched_loc_idx = loc_i
                        break

            # 3. Попытка сопоставления по названию песни
            if not matched_loc and clean_dz_title:
                for loc_i, loc in enumerate(local_tracks):
                    if loc_i in used_local_indices:
                        continue
                    if loc.title and normalize_clean_string(loc.title) == clean_dz_title:
                        matched_loc = loc
                        matched_loc_idx = loc_i
                        break

            if matched_loc and matched_loc_idx is not None:
                used_local_indices.add(matched_loc_idx)
                present_count += 1
                all_matched_tracks.append(
                    IncompleteTrackInfo(
                        track_number=dz_pos,
                        disc_number=dz_disc,
                        title=dz_title,
                        duration=dz_dur or matched_loc.duration,
                        is_present=True,
                        local_path=matched_loc.path,
                        deezer_id=dz_tid,
                        deezer_url=dz_url,
                    )
                )
            else:
                missing_numbers.append(dz_pos)
                trk_info = IncompleteTrackInfo(
                    track_number=dz_pos,
                    disc_number=dz_disc,
                    title=dz_title,
                    duration=dz_dur,
                    is_present=False,
                    local_path=None,
                    deezer_id=dz_tid,
                    deezer_url=dz_url,
                )
                missing_tracks.append(trk_info)
                all_matched_tracks.append(trk_info)

        res_item = IncompleteAlbumItem(
            path=norm_album_path,
            album=deezer_title or album_name,
            artist=deezer_artist or artist_name,
            year=year,
            has_cover=has_cover or bool(cover_url),
            cover_url=cover_url,
            present_count=present_count,
            expected_count=len(deezer_tracks),
            missing_track_numbers=missing_numbers,
            missing_tracks=missing_tracks,
            all_tracks=all_matched_tracks,
            is_catalog_matched=True,
            deezer_album_id=deezer_id,
        )
        self._catalog_cache[norm_album_path] = res_item
        return res_item

