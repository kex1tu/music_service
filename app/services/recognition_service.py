"""Сервис распознавания аудиофайлов (Recognition Service).

Обеспечивает акустическое распознавание неизвестных треков по аудиоотпечатку
через публичный API Shazam (библиотека shazamio) с извлечением названия,
исполнителя, альбома, года выпуска, жанра и официальной обложки в высоком качестве.
Все файловые операции изолированы через PathSandbox.
"""

import logging
import re
from pathlib import Path

from shazamio import Shazam

from app.core.exceptions import NotFoundError, PathTraversalError
from app.core.sandbox import PathSandbox
from app.schemas.tag import RecognizedTrackResponse

logger = logging.getLogger("music_manager.recognition")


class RecognitionService:
    """Сервис распознавания композиций через Shazam."""

    def __init__(
        self,
        music_sandbox: PathSandbox,
        tmp_sandbox: PathSandbox | None = None,
        shazam: Shazam | None = None,
    ) -> None:
        """Инициализирует сервис с внедрением песочниц и клиента Shazam."""
        self.music_sandbox = music_sandbox
        self.tmp_sandbox = tmp_sandbox
        self.shazam = shazam or Shazam(language="ru-RU", endpoint_country="RU")

    def _resolve_track_path(self, rel_path: str) -> Path:
        """Безопасно разрешает путь к MP3-файлу через песочницы медиатеки и временных загрузок."""
        try:
            music_path = self.music_sandbox.resolve(rel_path, allow_root=False)
            if music_path.exists():
                return music_path
        except PathTraversalError:
            pass

        if self.tmp_sandbox is not None:
            try:
                tmp_path = self.tmp_sandbox.resolve(rel_path, allow_root=False)
                if tmp_path.exists():
                    return tmp_path
            except PathTraversalError:
                pass

            # Поддержка путей, начинающихся с имени корневой директории tmp_sandbox
            normalized = rel_path.replace("\\", "/").lstrip("/")
            root_name = self.tmp_sandbox.root.name
            if normalized.startswith(f"{root_name}/"):
                stripped = normalized[len(root_name) + 1 :]
                try:
                    tmp_path = self.tmp_sandbox.resolve(stripped, allow_root=False)
                    if tmp_path.exists():
                        return tmp_path
                except PathTraversalError:
                    pass

        # Если не найден в tmp, пробуем разрешить через music_sandbox для валидации исключений
        return self.music_sandbox.resolve(rel_path, allow_root=False)

    async def recognize_audio(self, rel_track_path: str) -> RecognizedTrackResponse:
        """Распознает трек по его аудиосодержимому через Shazam.

        Args:
            rel_track_path: Относительный путь к MP3-файлу.

        Returns:
            RecognizedTrackResponse с метаданными или recognized=False при отсутствии совпадений.

        Raises:
            NotFoundError: Если файл не существует.
            PathTraversalError: При попытке выхода за пределы песочницы.
        """
        abs_path = self._resolve_track_path(rel_track_path)
        if not abs_path.exists() or not abs_path.is_file():
            raise NotFoundError(f"Файл для распознавания не найден: {rel_track_path}")

        logger.info("Запуск распознавания Shazam для файла: %s", abs_path.name)

        try:
            res = await self.shazam.recognize(str(abs_path))
        except Exception as exc:  # noqa: BLE001
            logger.warning("Сбой запроса к Shazam API для '%s': %s", rel_track_path, exc)
            return RecognizedTrackResponse(recognized=False)

        matches = res.get("matches", [])
        track_obj = res.get("track")
        if not matches or not track_obj:
            logger.info("Shazam не нашел совпадений для '%s'", abs_path.name)
            return RecognizedTrackResponse(recognized=False)

        title = track_obj.get("title")
        artist = track_obj.get("subtitle")

        album: str | None = None
        year: int | None = None

        # Извлечение метаданных из секций песни
        for section in track_obj.get("sections", []):
            if section.get("type") == "SONG":
                for item in section.get("metadata", []):
                    item_title = item.get("title", "").strip().lower()
                    item_text = item.get("text", "").strip()
                    if item_title == "album" and item_text:
                        album = item_text
                    elif item_title == "released" and item_text:
                        year_match = re.search(r"\b(19\d{2}|20\d{2})\b", item_text)
                        if year_match:
                            year = int(year_match.group(1))

        # Жанр
        genre: str | None = None
        genres_obj = track_obj.get("genres")
        if isinstance(genres_obj, dict):
            genre = genres_obj.get("primary")

        # Обложка
        images = track_obj.get("images", {})
        cover_url: str | None = images.get("coverarthq") or images.get("coverart")
        if cover_url and "mzstatic.com" in cover_url:
            # Увеличиваем разрешение обложки Apple/Shazam до 1000x1000
            cover_url = re.sub(r"/\d+x\d+cc\.", "/1000x1000cc.", cover_url)

        logger.info(
            "Shazam успешно распознал трек: %s — %s (Альбом: %s, Год: %s)",
            artist,
            title,
            album,
            year,
        )

        return RecognizedTrackResponse(
            recognized=True,
            title=title,
            artist=artist,
            album=album,
            year=year,
            genre=genre,
            cover_url=cover_url,
        )
