"""Сервис организации и размещения файлов медиатеки (Library Organizer Service).

Перемещает аудиофайлы, ассоциированные тексты песен (.lrc/.txt) и обложки
из временного буфера загрузок (TMP_DIR) в постоянную медиатеку (MUSIC_DIR),
а также реорганизует существующие треки при изменении тегов согласно строгому шаблону:
MUSIC_DIR/{AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.mp3

Все имена файлов и папок санитизируются через PathSandbox.validate_filename().
Опустевшие директории после переноса автоматически удаляются.
"""

import logging
import os
import re
import shutil
import threading
import uuid
from contextlib import ExitStack
from pathlib import Path

from pydantic import BaseModel, Field

from app.core.exceptions import (
    NotFoundError,
    PathTraversalError,
    StorageConfigurationError,
)
from app.core.sandbox import PathSandbox
from app.schemas.tag import AlbumTags, TrackTags
from app.services.tag_service import TagService

logger = logging.getLogger(__name__)

_album_locks: dict[str, threading.Lock] = {}
_album_locks_mutex = threading.Lock()


def _get_album_lock(album_dir: str) -> threading.Lock:
    """Возвращает или создает блокировку для синхронизации организации альбома."""
    with _album_locks_mutex:
        if album_dir not in _album_locks:
            _album_locks[album_dir] = threading.Lock()
        return _album_locks[album_dir]


class OrganizerResult(BaseModel):
    """Результат операции организации/перемещения файлов альбома."""

    destination_dir: str = Field(
        ..., description="Относительный путь целевого каталога альбома в MUSIC_DIR"
    )
    tracks: list[TrackTags] = Field(
        default_factory=list, description="Список треков с обновленными путями в MUSIC_DIR"
    )


class OrganizerService:
    """Сервис структурирования и перемещения файлов медиатеки."""

    def __init__(
        self,
        music_sandbox: PathSandbox,
        tmp_sandbox: PathSandbox | None = None,
        tag_service: TagService | None = None,
    ) -> None:
        """Инициализирует сервис песочницами и опциональным сервисом тегов."""
        self._music_sandbox = music_sandbox
        self._tmp_sandbox = tmp_sandbox
        self._tag_service = tag_service

    def sanitize_component(
        self,
        name: str | None,
        fallback: str = "Unknown",
        max_length: int = 180,
    ) -> str:
        """Санитизирует компонент пути (имя файла или папки).

        Заменяет запрещенные символы на безопасные, нормализует разделители
        множественных значений, ограничивает длину компонента и валидирует
        через PathSandbox.validate_filename().

        Args:
            name: Исходная строка имени компонента.
            fallback: Значение по умолчанию, если имя пустое или состоит из пробелов.
            max_length: Максимальная длина компонента в символах (защита от filesystem limits).

        Returns:
            Безопасное имя файла или каталога.
        """
        if not name:
            name = fallback

        # Заменяем множественные разделители вида '\\' на читаемое ' & '
        clean = re.sub(r"\\+|\x00", " & ", str(name)).strip()

        # Заменяем опасные символы (/ \ : * ? " < > | и управляющие коды) на '_'
        clean = PathSandbox.DANGEROUS_FILENAME_CHARS.sub("_", clean)

        # Удаляем ведущие и замыкающие точки и пробелы
        clean = clean.strip(". ")

        # Ограничиваем длину компонента для надежности файловых систем (ext4/NTFS/APFS limit 255 bytes)
        if len(clean) > max_length:
            clean = clean[:max_length].rstrip(". ")

        if not clean:
            clean = fallback

        # Гарантированная проверка через PathSandbox
        return PathSandbox.validate_filename(clean)

    def compute_album_dir(
        self,
        album: AlbumTags,
        tracks: list[TrackTags] | None = None,
    ) -> str:
        """Вычисляет канонический относительный путь каталога альбома внутри MUSIC_DIR.

        Шаблон: {AlbumArtist}/{Album} - {Year}
        Обеспечивает единый каталог для всех треков альбома (включая компиляции/сборники).
        """
        # 1. Исполнитель альбома (AlbumArtist)
        artist_raw = album.album_artist
        if not artist_raw and tracks:
            # Проверяем единообразие исполнителей среди треков альбома
            artists = {t.album_artist or t.artist for t in tracks if (t.album_artist or t.artist)}
            if len(artists) == 1:
                artist_raw = next(iter(artists))
            elif len(artists) > 1:
                artist_raw = "Various Artists"
            else:
                artist_raw = "Unknown Artist"
        elif not artist_raw:
            artist_raw = "Unknown Artist"

        clean_artist = self.sanitize_component(artist_raw, fallback="Unknown Artist")

        # 2. Название альбома (Album)
        album_raw = album.album
        if not album_raw and tracks:
            albums = {t.album for t in tracks if t.album}
            if len(albums) == 1:
                album_raw = next(iter(albums))
            else:
                album_raw = "Unknown Album"
        elif not album_raw:
            album_raw = "Unknown Album"

        clean_album = self.sanitize_component(album_raw, fallback="Unknown Album")

        # 3. Год выпуска (Year)
        year_val = album.year
        if not year_val and tracks:
            years = {t.year for t in tracks if t.year}
            if len(years) == 1:
                year_val = next(iter(years))

        if year_val and 1000 <= year_val <= 2999:
            # Проверяем, не содержит ли название альбома уже этот год в конце
            if clean_album.endswith(f" - {year_val}"):
                album_folder_name = clean_album
            else:
                album_folder_name = f"{clean_album} - {year_val}"
        else:
            album_folder_name = clean_album

        clean_album_folder = self.sanitize_component(album_folder_name, fallback=clean_album)
        return f"{clean_artist}/{clean_album_folder}"

    def compute_target_paths(
        self,
        album: AlbumTags,
        track: TrackTags,
    ) -> tuple[str, str]:
        """Вычисляет целевые относительные пути альбома и файла трека внутри MUSIC_DIR.

        Шаблон:
        {AlbumArtist}/{Album} - {Year}/{TrackNumber:02d}. {Title}.mp3

        Args:
            album: Метаданные альбома.
            track: Метаданные трека.

        Returns:
            Кортеж из (album_rel_dir, track_rel_path).
        """
        album_rel_dir = self.compute_album_dir(album, [track])

        # Имя файла трека ({TrackNumber:02d}. {Title}.mp3)
        track_num = max(1, track.track_number if track.track_number else 1)
        title_raw = track.title or Path(track.path).stem or f"Track_{track_num}"
        clean_title = self.sanitize_component(title_raw, fallback=f"Track_{track_num}")
        if clean_title.lower().endswith(".mp3"):
            clean_title = clean_title[:-4].rstrip(". ")
            if not clean_title:
                clean_title = f"Track_{track_num}"

        filename = f"{track_num:02d}. {clean_title}.mp3"
        clean_filename = self.sanitize_component(filename, fallback=f"{track_num:02d}. Track.mp3")

        track_rel_path = f"{album_rel_dir}/{clean_filename}"
        return album_rel_dir, track_rel_path

    def compute_single_dir(
        self,
        track: TrackTags,
        default_artist: str = "Unknown Artist",
    ) -> str:
        """Вычисляет канонический относительный путь каталога сингла внутри MUSIC_DIR.

        Шаблон: {Artist}/{Title} - Single ({Year}) или {Artist}/{Title} - Single
        """
        artist_raw = track.album_artist or track.artist or default_artist or "Unknown Artist"
        clean_artist = self.sanitize_component(artist_raw, fallback="Unknown Artist")

        title_raw = track.title or (Path(track.path).stem if track.path else "Track")
        clean_title = self.sanitize_component(title_raw, fallback="Track")

        if track.year and 1000 <= track.year <= 2999:
            folder_name = f"{clean_title} - Single ({track.year})"
        else:
            folder_name = f"{clean_title} - Single"

        clean_folder = self.sanitize_component(folder_name, fallback=folder_name)
        return f"{clean_artist}/{clean_folder}"

    def _resolve_source_path(self, rel_path: str) -> Path:
        """Разрешает исходный относительный путь через TagService или подходящую песочницу."""
        if self._tag_service is not None:
            return self._tag_service.resolve_path(rel_path, allow_root=True)

        rel_str = str(rel_path).strip()
        if self._tmp_sandbox is not None:
            if rel_str == "tmp_downloads" or rel_str.startswith("tmp_downloads/"):
                sub_path = rel_str[len("tmp_downloads") :].lstrip("/")
                return self._tmp_sandbox.resolve(sub_path, allow_root=True)
            if rel_str == "tmp" or rel_str.startswith("tmp/"):
                sub_path = rel_str[len("tmp") :].lstrip("/")
                try:
                    cand = self._tmp_sandbox.resolve(sub_path, allow_root=True)
                    if cand.exists():
                        return cand
                except (PathTraversalError, StorageConfigurationError, OSError):
                    pass
            try:
                candidate = self._tmp_sandbox.resolve(rel_str, allow_root=True)
                if candidate.exists():
                    return candidate
            except (PathTraversalError, StorageConfigurationError, OSError):
                pass

        music_root_name = self._music_sandbox.root.name
        if rel_str == "music_library" or rel_str == music_root_name:
            rel_str = ""
        elif rel_str.startswith("music_library/"):
            rel_str = rel_str[len("music_library/") :]
        elif rel_str.startswith(f"{music_root_name}/"):
            rel_str = rel_str[len(music_root_name) + 1 :]

        return self._music_sandbox.resolve(rel_str, allow_root=True)

    def _is_tmp_source(self, source_dir: str, tracks: list[TrackTags]) -> bool:
        """Определяет, является ли источник временным буфером загрузок (TMP_DIR)."""
        if self._tmp_sandbox is None:
            return False

        # 1. Проверяем расположение каталога источника
        if source_dir:
            try:
                cand = self._resolve_source_path(source_dir)
                if cand.is_relative_to(self._tmp_sandbox.root):
                    return True
            except (PathTraversalError, OSError) as exc:
                logger.debug("Проверка tmp пути источника: %s", exc)

        # 2. Проверяем расположение файлов треков
        for t in tracks:
            if not t.path:
                continue
            try:
                cand = self._resolve_source_path(t.path)
                if cand.is_relative_to(self._tmp_sandbox.root):
                    return True
            except (PathTraversalError, OSError) as exc:
                logger.debug("Проверка tmp пути трека: %s", exc)

        # 3. Виртуальные строковые префиксы задач загрузки
        s = str(source_dir).strip().replace("\\", "/")
        if s.startswith(("tmp_downloads", "tmp_downloads/")):
            return True
        return any(t.path.startswith(("tmp_downloads", "tmp_downloads/")) for t in tracks)

    def cleanup_empty_directories(self, start_dir: Path) -> None:
        """Рекурсивно удаляет пустые родительские директории снизу вверх до корня MUSIC_DIR.

        Никогда не удаляет сам корень песочницы. Зачищает скрытый мусор ОС (.DS_Store, Thumbs.db).
        """
        current = start_dir
        while (
            current != self._music_sandbox.root
            and current.is_relative_to(self._music_sandbox.root)
        ):
            if current.exists() and current.is_dir():
                try:
                    with os.scandir(current) as it:
                        entries = list(it)

                    # Отсеиваем служебный мусор операционных систем
                    substantive = [
                        e for e in entries
                        if e.name not in (".DS_Store", "Thumbs.db", "desktop.ini")
                        and not e.name.startswith(".stage_")
                    ]
                    if not substantive:
                        for e in entries:
                            try:
                                p = Path(e.path)
                                if p.is_dir():
                                    shutil.rmtree(p, ignore_errors=True)
                                else:
                                    p.unlink(missing_ok=True)
                            except OSError:
                                pass
                        current.rmdir()
                        logger.info("Удалена пустая директория: %s", current)
                    else:
                        break
                except OSError as exc:
                    logger.debug("Не удалось удалить директорию %s: %s", current, exc)
                    break
            current = current.parent

    def organize_album(
        self,
        album: AlbumTags,
        tracks: list[TrackTags],
        source_dir: str,
        cover_path: str | None = None,
        reorganize: bool = False,
        singles_mode: bool = False,
        group_by_album: bool = True,
        track_covers_data: dict[str, bytes] | None = None,
    ) -> OrganizerResult:
        """Перемещает или реорганизует файлы альбома в постоянную медиатеку MUSIC_DIR.

        Поддерживает:
        - Одиночные альбомы и мультиальбомные пакеты (group_by_album=True).
        - Режим синглов (singles_mode=True): треки раскладываются по {Artist}/{Title} - Single ({Year})/.
        - Запись cover.jpg для каждого релиза и сингла из track_covers_data или cover_path.
        - Двухфазный стейджинг (.stage_*) для защиты от потери данных и коллизий.
        - Автоматическую зачистку пустых директорий.

        Args:
            album: Метаданные альбома.
            tracks: Список треков для перемещения.
            source_dir: Исходный каталог (tmp job dir или существующий каталог).
            cover_path: Опциональный путь к файлу обложки альбома.
            reorganize: Флаг принудительной реорганизации существующей папки в медиатеке.
            singles_mode: Флаг режима синглов (каждый трек в персональную папку сингла).
            group_by_album: Флаг группировки разнородных треков по релизам.
            track_covers_data: Словарь с бинарными данными обложек {track_path: bytes}.

        Returns:
            OrganizerResult с итоговым каталогом и обновленными треками.
        """
        is_tmp = self._is_tmp_source(source_dir, tracks)

        # Если это существующий альбом в медиатеке и реорганизация не запрошена — оставляем пути как есть
        if not is_tmp and not reorganize:
            if cover_path:
                try:
                    src_cover = self._resolve_source_path(cover_path)
                    if src_cover.exists() and src_cover.is_file():
                        src_dir_abs = self._resolve_source_path(source_dir)
                        target_dir = src_dir_abs if src_dir_abs.is_dir() else src_dir_abs.parent
                        is_png = src_cover.suffix.lower() == ".png"
                        try:
                            if src_cover.read_bytes()[:8].startswith(b"\x89PNG"):
                                is_png = True
                        except OSError:
                            pass
                        target_cov = target_dir / ("cover.png" if is_png else "cover.jpg")
                        if src_cover.resolve() != target_cov.resolve():
                            shutil.copy2(str(src_cover), str(target_cov))
                            if src_cover.is_relative_to(self._music_sandbox.root):
                                src_cover.unlink(missing_ok=True)
                except (PathTraversalError, OSError) as exc:
                    logger.debug("Сбой сохранения cover_path при reorganize=False: %s", exc)
            return OrganizerResult(destination_dir=source_dir, tracks=tracks)

        if not tracks:
            dummy_track = TrackTags(path="", track_number=1, title="Track")
            target_album_rel_dir = self.compute_album_dir(album, [dummy_track])
            return OrganizerResult(destination_dir=target_album_rel_dir, tracks=[])

        # 1. Группировка треков по релизам (альбомы и синглы)
        groups: dict[str, dict] = {}

        if not singles_mode and not group_by_album:
            groups["__all__"] = {
                "key": "__all__",
                "is_single": False,
                "album_tags": album,
                "tracks": list(enumerate(tracks)),
            }
        else:
            for idx, track in enumerate(tracks):
                effective_album = "" if singles_mode else (track.album or album.album or "").strip()
                if singles_mode or not effective_album:
                    key = f"__single__{idx}_{track.path}"
                    groups[key] = {
                        "key": key,
                        "is_single": True,
                        "album_tags": None,
                        "tracks": [(idx, track)],
                    }
                else:
                    effective_artist = (track.album_artist or album.album_artist or "").strip()
                    key = f"__album__{effective_artist.lower()}__{effective_album.lower()}"
                    if key not in groups:
                        groups[key] = {
                            "key": key,
                            "is_single": False,
                            "album_tags": AlbumTags(
                                album=effective_album,
                                album_artist=effective_artist or album.album_artist,
                                year=track.year or album.year,
                                genre=track.genre or album.genre,
                                disc_total=track.disc_total or album.disc_total,
                            ),
                            "tracks": [],
                        }
                    groups[key]["tracks"].append((idx, track))

        for group in groups.values():
            if group["is_single"]:
                _, s_track = group["tracks"][0]
                group["target_rel_dir"] = self.compute_single_dir(
                    s_track,
                    default_artist=album.album_artist or album.album or "Unknown Artist",
                )
            else:
                group["target_rel_dir"] = self.compute_album_dir(
                    group["album_tags"],
                    [t for _, t in group["tracks"]],
                )

        unique_target_dirs = sorted({g["target_rel_dir"].lower() for g in groups.values()})

        with ExitStack() as stack:
            for t_dir in unique_target_dirs:
                stack.enter_context(_get_album_lock(t_dir))

            for group in groups.values():
                t_abs = self._music_sandbox.resolve(group["target_rel_dir"])
                t_abs.mkdir(parents=True, exist_ok=True)
                group["target_abs_dir"] = t_abs

            old_dirs_to_clean: set[Path] = set()
            if not is_tmp and source_dir:
                try:
                    src_dir_abs = self._resolve_source_path(source_dir)
                    if src_dir_abs.is_relative_to(self._music_sandbox.root):
                        old_dirs_to_clean.add(src_dir_abs if src_dir_abs.is_dir() else src_dir_abs.parent)
                except (PathTraversalError, OSError) as exc:
                    logger.debug("Проверка каталога источника: %s", exc)

            all_src_resolved: set[Path] = set()
            for t in tracks:
                if t.path:
                    try:
                        cand = self._resolve_source_path(t.path)
                        if cand.exists():
                            all_src_resolved.add(cand.resolve())
                    except (PathTraversalError, OSError):
                        pass

            # 2. Построение планов перемещения
            move_plans: list[dict] = []
            used_target_paths_by_group: dict[str, set[Path]] = {g["key"]: set() for g in groups.values()}

            for group in groups.values():
                used_target_paths = used_target_paths_by_group[group["key"]]
                target_group_rel_dir = group["target_rel_dir"]
                is_single_group = group["is_single"]

                for group_idx, (orig_idx, track) in enumerate(group["tracks"]):
                    src_track_abs = self._resolve_source_path(track.path)
                    if not src_track_abs.exists():
                        raise NotFoundError(f"Аудиофайл не найден: {track.path}")

                    if not is_tmp and src_track_abs.is_relative_to(self._music_sandbox.root):
                        old_dirs_to_clean.add(src_track_abs.parent)

                    if is_single_group:
                        track_num = max(1, track.track_number if (track.track_number and not singles_mode) else 1)
                        title_raw = track.title or Path(track.path).stem or "Track"
                        clean_title = self.sanitize_component(title_raw, fallback="Track", max_length=150)
                        if clean_title.lower().endswith(".mp3"):
                            clean_title = clean_title[:-4].rstrip(". ")
                            if not clean_title:
                                clean_title = "Track"
                        filename = f"01. {clean_title}.mp3" if singles_mode else f"{track_num:02d}. {clean_title}.mp3"
                    else:
                        track_num = max(1, track.track_number if track.track_number else group_idx + 1)
                        title_raw = track.title or Path(track.path).stem or f"Track_{track_num}"
                        clean_title = self.sanitize_component(title_raw, fallback=f"Track_{track_num}", max_length=150)
                        if clean_title.lower().endswith(".mp3"):
                            clean_title = clean_title[:-4].rstrip(". ")
                            if not clean_title:
                                clean_title = f"Track_{track_num}"
                        filename = f"{track_num:02d}. {clean_title}.mp3"

                    clean_filename = self.sanitize_component(filename, fallback=f"{track_num:02d}. Track.mp3", max_length=180)
                    target_track_rel_path = f"{target_group_rel_dir}/{clean_filename}"
                    target_track_abs = self._music_sandbox.resolve(target_track_rel_path)

                    dup_idx = 2
                    while (target_track_abs in used_target_paths) or (
                        target_track_abs.exists() and target_track_abs.resolve() not in all_src_resolved
                    ):
                        clean_filename = self.sanitize_component(
                            f"{track_num:02d}. {clean_title} ({dup_idx}).mp3",
                            fallback=f"{track_num:02d}. Track_{dup_idx}.mp3",
                            max_length=180,
                        )
                        target_track_rel_path = f"{target_group_rel_dir}/{clean_filename}"
                        target_track_abs = self._music_sandbox.resolve(target_track_rel_path)
                        dup_idx += 1

                    used_target_paths.add(target_track_abs)

                    lyrics_plans: list[tuple[Path, Path]] = []
                    for ext in (".lrc", ".txt"):
                        src_lyrics = src_track_abs.with_suffix(ext)
                        if src_lyrics.exists() and src_lyrics.is_file():
                            target_lyrics = target_track_abs.with_suffix(ext)
                            lyrics_plans.append((src_lyrics, target_lyrics))

                    move_plans.append({
                        "orig_idx": orig_idx,
                        "track": track,
                        "group": group,
                        "track_num": track_num,
                        "clean_title": clean_title,
                        "src_track_abs": src_track_abs,
                        "target_track_abs": target_track_abs,
                        "target_track_rel_path": target_track_rel_path,
                        "lyrics_plans": lyrics_plans,
                    })

            # 3. Фаза 1: Стейджинг
            stage_records: list[dict] = []
            try:
                for plan in move_plans:
                    src_abs: Path = plan["src_track_abs"]
                    target_abs: Path = plan["target_track_abs"]
                    stage_dir: Path = plan["group"]["target_abs_dir"]

                    if src_abs.resolve() != target_abs.resolve() or str(src_abs) != str(target_abs):
                        stage_track = stage_dir / f".stage_{uuid.uuid4().hex}_{target_abs.name[:64]}"
                        shutil.move(str(src_abs), str(stage_track))

                        staged_lyrics_list: list[tuple[Path, Path, Path]] = []
                        for s_lyr, t_lyr in plan["lyrics_plans"]:
                            if s_lyr.resolve() != t_lyr.resolve() or str(s_lyr) != str(t_lyr):
                                stage_lyr = stage_dir / f".stage_{uuid.uuid4().hex}_{t_lyr.name[:64]}"
                                shutil.move(str(s_lyr), str(stage_lyr))
                                staged_lyrics_list.append((stage_lyr, t_lyr, s_lyr))
                            else:
                                staged_lyrics_list.append((s_lyr, t_lyr, s_lyr))

                        stage_records.append({
                            "plan": plan,
                            "staged_track": stage_track,
                            "target_track": target_abs,
                            "staged_lyrics": staged_lyrics_list,
                        })
                    else:
                        stage_records.append({
                            "plan": plan,
                            "staged_track": None,
                            "target_track": target_abs,
                            "staged_lyrics": [(s, t, s) for s, t in plan["lyrics_plans"]],
                        })
            except Exception:
                for record in stage_records:
                    if record.get("staged_track") and record["staged_track"].exists():
                        try:
                            shutil.move(str(record["staged_track"]), str(record["plan"]["src_track_abs"]))
                        except Exception as rollback_err:  # noqa: BLE001
                            logger.warning("Сбой отката трека: %s", rollback_err)
                    for staged_lyr, _, orig_lyr in record.get("staged_lyrics", []):
                        if staged_lyr.exists() and staged_lyr != orig_lyr:
                            try:
                                shutil.move(str(staged_lyr), str(orig_lyr))
                            except Exception as rollback_err:  # noqa: BLE001
                                logger.warning("Сбой отката текста: %s", rollback_err)
                raise

            # 4. Фаза 2: Коммит
            updated_tracks_by_orig_idx: dict[int, TrackTags] = {}
            for record in stage_records:
                plan = record["plan"]
                target_abs = record["target_track"]

                if record["staged_track"] is not None:
                    shutil.move(str(record["staged_track"]), str(target_abs))
                    logger.info(
                        "Файл перемещен: %s -> %s",
                        plan["src_track_abs"].name,
                        plan["target_track_rel_path"],
                    )

                for staged_lyr, final_lyr, _ in record["staged_lyrics"]:
                    if staged_lyr.resolve() != final_lyr.resolve() or str(staged_lyr) != str(final_lyr):
                        shutil.move(str(staged_lyr), str(final_lyr))
                        logger.info(
                            "Текст песни перемещен: %s -> %s",
                            staged_lyr.name,
                            final_lyr.name,
                        )

                track: TrackTags = plan["track"]
                updated_track = track.model_copy()
                updated_track.path = plan["target_track_rel_path"]
                updated_track.track_number = plan["track_num"]

                group = plan["group"]
                if group["is_single"]:
                    updated_track.album = track.album or f"{plan['clean_title']} - Single"
                    updated_track.album_artist = track.album_artist or album.album_artist
                    updated_track.year = track.year or album.year
                    if album.genre:
                        updated_track.genre = track.genre or album.genre
                else:
                    g_album: AlbumTags = group["album_tags"]
                    updated_track.album = g_album.album or track.album
                    updated_track.album_artist = g_album.album_artist or track.album_artist
                    updated_track.year = g_album.year or track.year
                    if g_album.genre:
                        updated_track.genre = g_album.genre

                updated_tracks_by_orig_idx[plan["orig_idx"]] = updated_track

            # 5. Обработка обложек для каждого релиза
            def _choose_cover_target(src_file: Path, target_dir: Path) -> Path:
                try:
                    head = src_file.read_bytes()[:8]
                    if head.startswith(b"\x89PNG") or src_file.suffix.lower() == ".png":
                        return target_dir / "cover.png"
                except OSError:
                    pass
                return target_dir / "cover.jpg"

            for group in groups.values():
                target_dir = group["target_abs_dir"]
                if (target_dir / "cover.jpg").exists() or (target_dir / "cover.png").exists():
                    continue

                cover_written = False

                # Из track_covers_data (индивидуальные обложки треков/синглов)
                if track_covers_data:
                    for _, tr in group["tracks"]:
                        if track_covers_data.get(tr.path):
                            cov_data = track_covers_data[tr.path]
                            is_png = cov_data[:8].startswith(b"\x89PNG")
                            ext = "png" if is_png else "jpg"
                            cov_file = target_dir / f"cover.{ext}"
                            cov_file.write_bytes(cov_data)
                            cover_written = True
                            break

                # Из переданного cover_path (обложка альбома)
                if not cover_written and cover_path:
                    try:
                        src_cover = self._resolve_source_path(cover_path)
                        if src_cover.exists() and src_cover.is_file():
                            target_cover_abs = _choose_cover_target(src_cover, target_dir)
                            if src_cover.resolve() != target_cover_abs.resolve():
                                shutil.copy2(str(src_cover), str(target_cover_abs))
                            cover_written = True
                    except (PathTraversalError, OSError) as exc:
                        logger.debug("Сбой обработки cover_path для группы: %s", exc)

                # Из файлов источника (cover.jpg/png и т.д.)
                if not cover_written:
                    try:
                        src_dir_abs = self._resolve_source_path(source_dir)
                        dir_to_check = src_dir_abs if src_dir_abs.is_dir() else src_dir_abs.parent
                        if dir_to_check.is_dir():
                            candidates = (
                                "cover.jpg",
                                "cover.jpeg",
                                "cover.png",
                                "folder.jpg",
                                "folder.jpeg",
                                "folder.png",
                                "front.jpg",
                                "front.png",
                            )
                            for cand_name in candidates:
                                cand_file = dir_to_check / cand_name
                                if cand_file.exists() and cand_file.is_file():
                                    target_cover_abs = _choose_cover_target(cand_file, target_dir)
                                    if cand_file.resolve() != target_cover_abs.resolve():
                                        shutil.copy2(str(cand_file), str(target_cover_abs))
                                    cover_written = True
                                    break
                    except (PathTraversalError, OSError) as exc:
                        logger.debug("Сбой поиска обложки в source_dir: %s", exc)

            # Если cover_path был в медиатеке и реорганизуется — зачищаем старый файл
            if cover_path and not is_tmp:
                try:
                    src_cover = self._resolve_source_path(cover_path)
                    if (
                        src_cover.exists()
                        and src_cover.is_file()
                        and src_cover.is_relative_to(self._music_sandbox.root)
                        and not any(src_cover.resolve() == (g["target_abs_dir"] / src_cover.name).resolve() for g in groups.values())
                    ):
                        src_cover.unlink(missing_ok=True)
                except (PathTraversalError, OSError):
                    pass

            # 6. Очистка исходных директорий
            if is_tmp and self._tmp_sandbox:
                job_roots_to_clean: set[Path] = set()
                if source_dir:
                    try:
                        src_dir_abs = self._resolve_source_path(source_dir)
                        if src_dir_abs.is_relative_to(self._tmp_sandbox.root):
                            rel_to_tmp = self._tmp_sandbox.to_relative(src_dir_abs)
                            job_id = rel_to_tmp.parts[0] if rel_to_tmp.parts else None
                            if job_id and job_id not in (".", ".."):
                                job_root = self._tmp_sandbox.resolve(job_id)
                                if job_root.exists() and job_root != self._tmp_sandbox.root:
                                    job_roots_to_clean.add(job_root)
                    except (PathTraversalError, OSError) as exc:
                        logger.debug("Сбой разрешения source_dir для очистки tmp: %s", exc)

                for t in tracks:
                    if not t.path:
                        continue
                    try:
                        src_path_abs = self._resolve_source_path(t.path)
                        if src_path_abs.is_relative_to(self._tmp_sandbox.root):
                            rel_to_tmp = self._tmp_sandbox.to_relative(src_path_abs)
                            job_id = rel_to_tmp.parts[0] if rel_to_tmp.parts else None
                            if job_id and job_id not in (".", ".."):
                                job_root = self._tmp_sandbox.resolve(job_id)
                                if job_root.exists() and job_root != self._tmp_sandbox.root:
                                    job_roots_to_clean.add(job_root)
                    except (PathTraversalError, OSError) as exc:
                        logger.debug("Сбой разрешения трека для очистки tmp: %s", exc)

                for job_root in job_roots_to_clean:
                    try:
                        shutil.rmtree(job_root, ignore_errors=True)
                        logger.info("Удален временный каталог загрузки: %s", job_root.name)
                    except OSError as exc:
                        logger.debug("Не удалось удалить временный каталог %s: %s", job_root, exc)
            elif not is_tmp:
                for old_dir in old_dirs_to_clean:
                    self.cleanup_empty_directories(old_dir)

            # 7. Вычисление результирующего destination_dir
            all_target_dirs = [g["target_rel_dir"] for g in groups.values()]
            if len(all_target_dirs) == 1:
                destination_dir = all_target_dirs[0]
            else:
                try:
                    common = os.path.commonpath(all_target_dirs)
                    destination_dir = common if common else all_target_dirs[0]
                except ValueError:
                    destination_dir = all_target_dirs[0]

            return OrganizerResult(
                destination_dir=destination_dir,
                tracks=[updated_tracks_by_orig_idx[i] for i in range(len(tracks))],
            )
