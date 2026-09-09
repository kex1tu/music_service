"""Маршруты API для файлового менеджера и навигации по медиатеке (Files Router).

Предоставляет эндпоинты:
- GET /files: получение содержимого каталога (папки и MP3-треки с базовыми метаданными).
- GET /files/tracks: получение детального списка треков альбома с полными тегами.
- DELETE /files: безопасное удаление файлов или папок внутри постоянной медиатеки MUSIC_DIR.

Все эндпоинты защищены сессионной аутентификацией CurrentUserDep и песочницей PathSandbox.
"""

import logging

from fastapi import APIRouter, Query

from app.dependencies import CurrentUserDep, FileManagerServiceDep
from app.schemas.files import DeleteFileResponse, DirectoryListingResponse
from app.schemas.tag import TrackTags

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/files", tags=["files"])


@router.get(
    "",
    response_model=DirectoryListingResponse,
    summary="Получение списка элементов директории медиатеки",
    description="Возвращает списки подкаталогов и файлов в указанном пути внутри MUSIC_DIR.",
)
def list_directory(
    path: str = Query(
        default="",
        description="Относительный путь к каталогу внутри MUSIC_DIR (пустая строка для корня)",
    ),
    _: CurrentUserDep = ...,
    file_manager: FileManagerServiceDep = ...,
) -> DirectoryListingResponse:
    """Возвращает содержимое указанного каталога медиатеки с метаданными треков."""
    return file_manager.list_dir(path)


@router.get(
    "/tracks",
    response_model=list[TrackTags],
    summary="Получение списка треков альбома",
    description="Считывает ID3v2.4 теги всех треков в указанной папке альбома.",
)
def get_album_tracks(
    path: str = Query(
        ...,
        description="Относительный путь к папке альбома или отдельному аудиофайлу",
    ),
    _: CurrentUserDep = ...,
    file_manager: FileManagerServiceDep = ...,
) -> list[TrackTags]:
    """Возвращает треклист альбома, упорядоченный по номерам дорожек."""
    return file_manager.get_album_tracks(path)


@router.delete(
    "",
    response_model=DeleteFileResponse,
    summary="Удаление файла или папки из медиатеки",
    description="Безопасно удаляет файл или каталог внутри MUSIC_DIR с защитой от удаления корня.",
)
def delete_file_or_directory(
    path: str = Query(
        ...,
        description="Относительный путь к удаляемому файлу или каталогу внутри MUSIC_DIR",
    ),
    _: CurrentUserDep = ...,
    file_manager: FileManagerServiceDep = ...,
) -> DeleteFileResponse:
    """Удаляет файл или директорию из медиатеки с проверкой песочницы."""
    return file_manager.delete(path)
