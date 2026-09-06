"""Доменные исключения приложения Music Downloader & Tag Manager.

Все доменные исключения наследуются от MusicManagerError и не зависят
от HTTP-фреймворков (FastAPI/Starlette). Трансляция в HTTP-статусы
осуществляется глобальным обработчиком исключений в app/main.py.
"""

from typing import Any


class MusicManagerError(Exception):
    """Базовое доменное исключение приложения."""

    status_code: int = 500
    error_code: str = "INTERNAL_SERVER_ERROR"

    def __init__(self, message: str, details: dict[str, Any] | None = None) -> None:
        super().__init__(message)
        self.message = message
        self.details = details or {}

    def to_dict(self) -> dict[str, Any]:
        """Сериализация исключения для JSON-ответа."""
        payload: dict[str, Any] = {
            "error": self.error_code,
            "message": self.message,
        }
        if self.details:
            payload["details"] = self.details
        return payload


class StorageConfigurationError(MusicManagerError):
    """Ошибка валидации путей или прав доступа к MUSIC_DIR / TMP_DIR при старте."""

    status_code = 500
    error_code = "STORAGE_CONFIG_ERROR"


class PathTraversalError(MusicManagerError):
    """Попытка выхода за пределы безопасного корня песочницы."""

    status_code = 403
    error_code = "PATH_TRAVERSAL_DETECTED"


class StoragePermissionError(MusicManagerError):
    """Ошибка прав доступа к файлу или каталогу при операциях с диском."""

    status_code = 403
    error_code = "STORAGE_PERMISSION_DENIED"


class NotFoundError(MusicManagerError):
    """Запрошенный ресурс (трек, альбом, задача загрузки) не найден."""

    status_code = 404
    error_code = "RESOURCE_NOT_FOUND"


class DownloadError(MusicManagerError):
    """Сбой в процессе загрузки или конвертации аудиопотока."""

    status_code = 502
    error_code = "DOWNLOAD_FAILED"


class TagError(MusicManagerError):
    """Базовая ошибка обработки метаданных ID3."""

    status_code = 422
    error_code = "TAG_PROCESSING_ERROR"


class TagWriteError(TagError):
    """Невозможно записать ID3v2.4 теги или встроить обложку."""

    status_code = 422
    error_code = "TAG_WRITE_FAILED"


class AuthenticationError(MusicManagerError):
    """Ошибка аутентификации или недействительная сессия."""

    status_code = 401
    error_code = "UNAUTHORIZED"
