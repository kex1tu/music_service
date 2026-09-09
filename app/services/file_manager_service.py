"""Сервис файлового менеджера и навигации по медиатеке (File Manager Service).

Предоставляет методы безопасного исследования каталогов, ленивой загрузки
содержимого папок, считывания треков альбомов и защищенного удаления файлов
и директорий строго в рамках постоянной медиатеки MUSIC_DIR.
Все операции изолированы через PathSandbox.
"""

import logging
import os
import shutil
from pathlib import Path

from app.core.exceptions import (
    NotFoundError,
    PathTraversalError,
)
from app.core.sandbox import PathSandbox
from app.schemas.files import (
    BreadcrumbItem,
    DeleteFileResponse,
    DirectoryListingResponse,
    FileItem,
)
from app.schemas.tag import TrackTags
from app.services.tag_service import TagService

logger = logging.getLogger(__name__)


class FileManagerService:
    """Сервис для навигации, чтения метаданных и управления файлами в MUSIC_DIR."""

    def __init__(
        self,
        music_sandbox: PathSandbox,
        tag_service: TagService | None = None,
    ) -> None:
        """Инициализирует сервис песочницей медиатеки и опциональным TagService."""
        self._music_sandbox = music_sandbox
        self._tag_service = tag_service

    def list_dir(self, rel_path: str = "") -> DirectoryListingResponse:
        """Возвращает структурированное содержимое каталога в медиатеке.

        Args:
            rel_path: Относительный путь к каталогу внутри MUSIC_DIR.

        Returns:
            DirectoryListingResponse со списками подпапок и файлов с метаданными.

        Raises:
            NotFoundError: Если каталог не существует или путь указывает на файл.
            PathTraversalError: При попытке выхода за пределы MUSIC_DIR.
        """
        resolved_dir = self._music_sandbox.resolve(rel_path, allow_root=True)
        if not resolved_dir.exists():
            raise NotFoundError(f"Каталог не найден: {rel_path}")
        if not resolved_dir.is_dir():
            raise NotFoundError(f"Путь '{rel_path}' не является директорией")

        # Вычисляем канонический относительный путь текущего каталога
        if resolved_dir == self._music_sandbox.root:
            norm_current = ""
            parent_path: str | None = None
        else:
            norm_current = str(self._music_sandbox.to_relative(resolved_dir)).replace("\\", "/")
            if resolved_dir.parent == self._music_sandbox.root:
                parent_path = ""
            else:
                parent_path = str(self._music_sandbox.to_relative(resolved_dir.parent)).replace(
                    "\\", "/"
                )

        # Формируем навигационную цепочку хлебных крошек
        breadcrumbs: list[BreadcrumbItem] = [BreadcrumbItem(name="Медиатека", path="")]
        if norm_current:
            segments = norm_current.split("/")
            accum = ""
            for seg in segments:
                if seg:
                    accum = f"{accum}/{seg}".lstrip("/")
                    breadcrumbs.append(BreadcrumbItem(name=seg, path=accum))

        # Сканируем содержимое каталога (скрытые файлы и служебные папки отсеиваются)
        raw_entries: list[os.DirEntry] = []
        try:
            with os.scandir(resolved_dir) as it:
                raw_entries = [e for e in it if not e.name.startswith(".")]
        except OSError as exc:
            logger.warning("Ошибка чтения каталога '%s': %s", resolved_dir, exc)
            raw_entries = []

        # Разделяем на директории и файлы, сортируем по алфавиту
        dir_entries = sorted(
            [e for e in raw_entries if e.is_dir(follow_symlinks=False)],
            key=lambda e: e.name.lower(),
        )
        file_entries = sorted(
            [e for e in raw_entries if e.is_file(follow_symlinks=False)],
            key=lambda e: e.name.lower(),
        )

        items: list[FileItem] = []

        # Обработка директорий
        for d in dir_entries:
            child_rel = f"{norm_current}/{d.name}".lstrip("/")
            child_abs = self._music_sandbox.resolve_child(child_rel)

            # Подсчитываем количество MP3 файлов в поддиректории (прямые или вложенные альбомы)
            mp3_count = 0
            try:
                with os.scandir(child_abs) as sub_it:
                    mp3_count = sum(
                        1
                        for sub in sub_it
                        if sub.is_file(follow_symlinks=False)
                        and sub.name.lower().endswith(".mp3")
                        and not sub.name.startswith(".")
                    )
                if mp3_count == 0:
                    mp3_count = sum(
                        1
                        for p in child_abs.rglob("*.mp3")
                        if p.is_file() and not p.name.startswith(".")
                    )
            except OSError:
                mp3_count = 0

            items.append(
                FileItem(
                    name=d.name,
                    path=child_rel,
                    type="directory",
                    track_count=mp3_count,
                )
            )

        # Обработка файлов
        for f in file_entries:
            child_rel = f"{norm_current}/{f.name}".lstrip("/")
            child_abs = self._music_sandbox.resolve_child(child_rel)
            ext = Path(f.name).suffix.lower()

            try:
                st = f.stat(follow_symlinks=False)
                file_size = st.st_size
            except OSError:
                file_size = None

            if ext == ".mp3" and self._tag_service is not None:
                try:
                    tags = self._tag_service.read_tags(child_abs, rel_path=child_rel)
                    items.append(
                        FileItem(
                            name=f.name,
                            path=child_rel,
                            type="file",
                            size=file_size,
                            extension=ext,
                            track_number=tags.track_number,
                            title=tags.title or Path(f.name).stem,
                            artist=tags.artist,
                            album=tags.album,
                            duration=tags.duration,
                            has_cover=tags.has_cover,
                            lyrics_status=tags.lyrics_status,
                        )
                    )
                except Exception as exc:  # noqa: BLE001
                    logger.debug("Сбой чтения тегов файла '%s': %s", f.name, exc)
                    items.append(
                        FileItem(
                            name=f.name,
                            path=child_rel,
                            type="file",
                            size=file_size,
                            extension=ext,
                            title=Path(f.name).stem,
                        )
                    )
            else:
                items.append(
                    FileItem(
                        name=f.name,
                        path=child_rel,
                        type="file",
                        size=file_size,
                        extension=ext,
                    )
                )

        return DirectoryListingResponse(
            current_path=norm_current,
            parent_path=parent_path,
            breadcrumbs=breadcrumbs,
            items=items,
            total_directories=len(dir_entries),
            total_files=len(file_entries),
        )

    def get_album_tracks(self, rel_album_path: str) -> list[TrackTags]:
        """Возвращает список треков альбома с тегами для отображения в браузере файлов.

        Args:
            rel_album_path: Относительный путь к папке альбома или отдельному MP3.

        Returns:
            Список объектов TrackTags, отсортированных по номеру дорожки.
        """
        resolved = self._music_sandbox.resolve(rel_album_path, allow_root=False)
        if not resolved.exists():
            raise NotFoundError(f"Файл или каталог не найден: {rel_album_path}")

        if not self._tag_service:
            return []

        if resolved.is_file():
            if resolved.suffix.lower() == ".mp3":
                return [self._tag_service.read_tags(resolved, rel_path=rel_album_path)]
            return []

        # Каталог альбома
        details = self._tag_service.read_album_details(rel_album_path)
        return details.tracks

    def delete(self, rel_path: str) -> DeleteFileResponse:
        """Безопасно удаляет файл или папку внутри постоянной медиатеки MUSIC_DIR.

        Защищает корень медиатеки от удаления, проверяет изоляцию через PathSandbox,
        а при удалении MP3 также удаляет ассоциированные файлы текстов песен (.lrc/.txt).

        Args:
            rel_path: Относительный путь к удаляемому файлу или каталогу.

        Returns:
            DeleteFileResponse с подтверждением удаления.

        Raises:
            PathTraversalError: При попытке удаления корневого каталога или обхода песочницы.
            NotFoundError: Если целевой объект не существует.
        """
        if rel_path is None:
            raise PathTraversalError("Путь не может быть пустым (None)")

        cleaned = str(rel_path).strip().replace("\\", "/").strip("/")
        if not cleaned or cleaned in (".", ".."):
            raise PathTraversalError("Удаление корневой директории медиатеки запрещено")

        resolved = self._music_sandbox.resolve(cleaned, allow_root=False)
        if resolved == self._music_sandbox.root:
            raise PathTraversalError("Удаление корневой директории медиатеки запрещено")

        if not resolved.exists():
            raise NotFoundError(f"Файл или каталог не найден: {rel_path}")

        is_directory = resolved.is_dir()
        if is_directory:
            shutil.rmtree(resolved)
            logger.info("Удален каталог медиатеки: %s", cleaned)
        else:
            resolved.unlink()
            logger.info("Удален файл медиатеки: %s", cleaned)
            # При удалении MP3 проверяем и удаляем прилежащие тексты песен
            if resolved.suffix.lower() == ".mp3":
                for ext in (".lrc", ".txt"):
                    lyrics_path = resolved.with_suffix(ext)
                    if lyrics_path.exists() and lyrics_path.is_file():
                        lyrics_path.unlink()
                        logger.info("Удален ассоциированный файл текста: %s", lyrics_path.name)

        return DeleteFileResponse(
            success=True,
            deleted_path=cleaned,
            is_directory=is_directory,
        )
