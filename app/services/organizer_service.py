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
    ) -> OrganizerResult:
        """Перемещает или реорганизует файлы альбома в постоянную медиатеку MUSIC_DIR.

        Использует двухфазный стейджинг для защиты от потери данных при изменении порядка
        (swap), циклическом переименовании или совпадении имен треков.
        Если файлы находятся в TMP_DIR — они безусловно перемещаются в постоянную медиатеку.
        Если файлы уже находятся в MUSIC_DIR — перемещение выполняется только если reorganize=True.
        При перемещении переносятся ассоциированные тексты песен (.lrc/.txt) и обложка (cover.jpg).
        Пустые старые папки удаляются.

        Args:
            album: Метаданные альбома.
            tracks: Список треков для перемещения.
            source_dir: Исходный каталог (tmp job dir или существующий каталог).
            cover_path: Опциональный путь к файлу обложки.
            reorganize: Флаг принудительной реорганизации существующей папки в медиатеке.

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
            # Если треков нет, но запрошена реорганизация — возвращаем целевой путь каталога
            dummy_track = TrackTags(path="", track_number=1, title="Track")
            target_album_rel_dir = self.compute_album_dir(album, [dummy_track])
            return OrganizerResult(destination_dir=target_album_rel_dir, tracks=[])

        # Единый целевой каталог для всех треков альбома
        target_album_rel_dir = self.compute_album_dir(album, tracks)

        with _get_album_lock(target_album_rel_dir.lower()):
            target_album_abs_dir = self._music_sandbox.resolve(target_album_rel_dir)
            target_album_abs_dir.mkdir(parents=True, exist_ok=True)

            old_dirs_to_clean: set[Path] = set()
            if not is_tmp and source_dir:
                try:
                    src_dir_abs = self._resolve_source_path(source_dir)
                    if src_dir_abs.is_relative_to(self._music_sandbox.root):
                        old_dirs_to_clean.add(src_dir_abs if src_dir_abs.is_dir() else src_dir_abs.parent)
                except (PathTraversalError, OSError) as exc:
                    logger.debug("Проверка каталога источника: %s", exc)

            # Собираем канонические пути всех исходных файлов текущего батча для защиты от коллизий
            all_src_resolved: set[Path] = set()
            for t in tracks:
                if t.path:
                    try:
                        cand = self._resolve_source_path(t.path)
                        if cand.exists():
                            all_src_resolved.add(cand.resolve())
                    except (PathTraversalError, OSError):
                        pass

            # 1. Валидация источников и построение плана перемещения с защитой от дублирования имен
            move_plans: list[dict] = []
            used_target_paths: set[Path] = set()

            for idx, track in enumerate(tracks):
                src_track_abs = self._resolve_source_path(track.path)
                if not src_track_abs.exists():
                    raise NotFoundError(f"Аудиофайл не найден: {track.path}")

                if not is_tmp and src_track_abs.is_relative_to(self._music_sandbox.root):
                    old_dirs_to_clean.add(src_track_abs.parent)

                track_num = max(1, track.track_number if track.track_number else idx + 1)
                title_raw = track.title or Path(track.path).stem or f"Track_{track_num}"
                clean_title = self.sanitize_component(title_raw, fallback=f"Track_{track_num}", max_length=150)
                if clean_title.lower().endswith(".mp3"):
                    clean_title = clean_title[:-4].rstrip(". ")
                    if not clean_title:
                        clean_title = f"Track_{track_num}"

                filename = f"{track_num:02d}. {clean_title}.mp3"
                clean_filename = self.sanitize_component(filename, fallback=f"{track_num:02d}. Track.mp3", max_length=180)
                target_track_rel_path = f"{target_album_rel_dir}/{clean_filename}"
                target_track_abs = self._music_sandbox.resolve(target_track_rel_path)

                # Дедупликация target_track_abs внутри батча и от независимых файлов на диске
                dup_idx = 2
                while (target_track_abs in used_target_paths) or (
                    target_track_abs.exists() and target_track_abs.resolve() not in all_src_resolved
                ):
                    clean_filename = self.sanitize_component(
                        f"{track_num:02d}. {clean_title} ({dup_idx}).mp3",
                        fallback=f"{track_num:02d}. Track_{dup_idx}.mp3",
                        max_length=180,
                    )
                    target_track_rel_path = f"{target_album_rel_dir}/{clean_filename}"
                    target_track_abs = self._music_sandbox.resolve(target_track_rel_path)
                    dup_idx += 1

                used_target_paths.add(target_track_abs)

                # Ассоциированные тексты песен (.lrc и .txt)
                lyrics_plans: list[tuple[Path, Path]] = []
                for ext in (".lrc", ".txt"):
                    src_lyrics = src_track_abs.with_suffix(ext)
                    if src_lyrics.exists() and src_lyrics.is_file():
                        target_lyrics = target_track_abs.with_suffix(ext)
                        lyrics_plans.append((src_lyrics, target_lyrics))

                move_plans.append({
                    "track": track,
                    "src_track_abs": src_track_abs,
                    "target_track_abs": target_track_abs,
                    "target_track_rel_path": target_track_rel_path,
                    "lyrics_plans": lyrics_plans,
                })

            # 2. Фаза 1: Стейджинг перемещаемых файлов во временные имена
            # Освобождает исходные пути, устраняя перезапись при смене номеров дорожек местами (swap)
            # и обеспечивая корректное переименование регистра (case-only rename).
            # Защищен откатом при любых непредвиденных ошибках ввода-вывода (гарантия сохранности данных).
            stage_records: list[dict] = []
            try:
                for plan in move_plans:
                    src_abs: Path = plan["src_track_abs"]
                    target_abs: Path = plan["target_track_abs"]

                    if src_abs.resolve() != target_abs.resolve() or str(src_abs) != str(target_abs):
                        stage_track = target_album_abs_dir / f".stage_{uuid.uuid4().hex}_{target_abs.name[:64]}"
                        shutil.move(str(src_abs), str(stage_track))

                        staged_lyrics_list: list[tuple[Path, Path, Path]] = []
                        for s_lyr, t_lyr in plan["lyrics_plans"]:
                            if s_lyr.resolve() != t_lyr.resolve() or str(s_lyr) != str(t_lyr):
                                stage_lyr = target_album_abs_dir / f".stage_{uuid.uuid4().hex}_{t_lyr.name[:64]}"
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
                # Откат стейджинга при сбое (защита от потери исходных файлов пользователя)
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

            # 3. Фаза 2: Коммит из стейджинга в финальные пути
            updated_tracks: list[TrackTags] = []
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
                updated_track.album = album.album or track.album
                updated_track.album_artist = album.album_artist or track.album_artist
                updated_track.year = album.year or track.year
                if album.genre:
                    updated_track.genre = album.genre
                updated_tracks.append(updated_track)

            # 4. Обработка обложки альбома (cover.jpg / cover.png)
            def _choose_cover_target(src_file: Path) -> Path:
                try:
                    head = src_file.read_bytes()[:8]
                    if head.startswith(b"\x89PNG") or src_file.suffix.lower() == ".png":
                        return target_album_abs_dir / "cover.png"
                except OSError:
                    pass
                return target_album_abs_dir / "cover.jpg"

            target_cover_abs = target_album_abs_dir / "cover.jpg"
            cover_handled = False

            if cover_path:
                try:
                    src_cover = self._resolve_source_path(cover_path)
                    if src_cover.exists() and src_cover.is_file():
                        target_cover_abs = _choose_cover_target(src_cover)
                        if src_cover.resolve() != target_cover_abs.resolve():
                            shutil.copy2(str(src_cover), str(target_cover_abs))
                            # При перемещении из библиотеки зачищаем старую обложку для удаления пустой папки
                            if not is_tmp and src_cover.is_relative_to(self._music_sandbox.root):
                                try:
                                    src_cover.unlink()
                                except OSError:
                                    pass
                        cover_handled = True
                except (PathTraversalError, OSError) as exc:
                    logger.debug("Сбой обработки cover_path '%s': %s", cover_path, exc)

            if not cover_handled and not (target_album_abs_dir / "cover.jpg").exists() and not (target_album_abs_dir / "cover.png").exists():
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
                                target_cover_abs = _choose_cover_target(cand_file)
                                if cand_file.resolve() != target_cover_abs.resolve():
                                    shutil.copy2(str(cand_file), str(target_cover_abs))
                                    if not is_tmp and cand_file.is_relative_to(self._music_sandbox.root):
                                        try:
                                            cand_file.unlink()
                                        except OSError:
                                            pass
                                break
                except (PathTraversalError, OSError) as exc:
                    logger.debug("Сбой поиска обложки в source_dir '%s': %s", source_dir, exc)

            # 5. Очистка исходных директорий
            if is_tmp:
                try:
                    src_dir_abs = self._resolve_source_path(source_dir)
                    if self._tmp_sandbox and src_dir_abs.is_relative_to(self._tmp_sandbox.root):
                        rel_to_tmp = self._tmp_sandbox.to_relative(src_dir_abs)
                        job_id = rel_to_tmp.parts[0] if rel_to_tmp.parts else None
                        if job_id:
                            job_root = self._tmp_sandbox.resolve(job_id)
                            if job_root.exists() and job_root != self._tmp_sandbox.root:
                                shutil.rmtree(job_root, ignore_errors=True)
                                logger.info("Удален временный каталог загрузки: %s", job_id)
                except (PathTraversalError, OSError) as exc:
                    logger.debug("Сбой очистки временного каталога задачи: %s", exc)
            else:
                for old_dir in old_dirs_to_clean:
                    self.cleanup_empty_directories(old_dir)

            return OrganizerResult(
                destination_dir=target_album_rel_dir,
                tracks=updated_tracks,
            )
