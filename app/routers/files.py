"""Маршруты API для файлового менеджера и навигации по медиатеке (Files Router).

Предоставляет эндпоинты:
- GET /files: получение содержимого каталога (папки и MP3-треки с базовыми метаданными).
- GET /files/tracks: получение детального списка треков альбома с полными тегами.
- DELETE /files: безопасное удаление файлов или папок внутри постоянной медиатеки MUSIC_DIR.

Все эндпоинты защищены сессионной аутентификацией CurrentUserDep и песочницей PathSandbox.
"""

import logging

from fastapi import APIRouter, Query

from app.dependencies import (
    CurrentUserDep,
    FileManagerServiceDep,
    LibraryAuditServiceDep,
)
from app.schemas.files import DeleteFileResponse, DirectoryListingResponse
from app.schemas.library_audit import (
    AuditSummaryResponse,
    BatchLyricsProgressResponse,
    DuplicatesResponse,
    LibrarySearchResponse,
    MissingCoverItem,
    MissingLyricsItem,
)
from app.schemas.tag import TrackTags

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/files", tags=["files"])


@router.get(
    "/search",
    response_model=LibrarySearchResponse,
    summary="Глобальный поиск по медиатеке",
    description="Выполняет быстрый поиск по артистам, альбомам и названиям треков.",
)
async def search_library(
    query: str = Query(..., min_length=1, description="Поисковый запрос"),
    limit: int = Query(default=20, ge=1, le=100, description="Максимальное количество результатов в каждой категории"),
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> LibrarySearchResponse:
    """Возвращает сгруппированные результаты поиска по медиатеке."""
    return await audit_service.search(query=query, limit=limit)


@router.get(
    "/audit/summary",
    response_model=AuditSummaryResponse,
    summary="Сводные метрики аудита медиатеки",
    description="Возвращает общее число треков, альбомов, отсутствующих текстов, обложек и дубликатов.",
)
async def get_audit_summary(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> AuditSummaryResponse:
    """Возвращает дашборд-сводку аудита медиатеки."""
    return await audit_service.get_summary()


@router.get(
    "/audit/missing-lyrics",
    response_model=list[MissingLyricsItem],
    summary="Список треков без текстов песен",
    description="Возвращает список MP3 треков, рядом с которыми нет файлов .lrc или .txt.",
)
async def get_missing_lyrics(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> list[MissingLyricsItem]:
    """Возвращает треки без текстов."""
    return await audit_service.get_missing_lyrics()


@router.post(
    "/audit/missing-lyrics/fetch-all",
    response_model=BatchLyricsProgressResponse,
    summary="Запуск пакетного поиска недостающих текстов",
    description="Запускает фоновый сбор текстов через LRCLIB и Genius для всех треков без слов.",
)
async def start_batch_lyrics_fetch(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> BatchLyricsProgressResponse:
    """Инициирует фоновый поиск текстов песен."""
    return audit_service.start_batch_lyrics_fetch()


@router.get(
    "/audit/missing-lyrics/progress",
    response_model=BatchLyricsProgressResponse,
    summary="Прогресс пакетного поиска текстов",
    description="Возвращает текущее состояние и счетчик прогресса фонового поиска текстов.",
)
async def get_batch_lyrics_progress(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> BatchLyricsProgressResponse:
    """Возвращает текущий прогресс автопоиска текстов."""
    return audit_service.get_batch_lyrics_progress()


@router.get(
    "/audit/missing-covers",
    response_model=list[MissingCoverItem],
    summary="Список релизов и треков без обложек",
    description="Возвращает папки альбомов и отдельные треки, у которых отсутствует графическая обложка.",
)
async def get_missing_covers(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> list[MissingCoverItem]:
    """Возвращает релизы без обложек."""
    return await audit_service.get_missing_covers()


@router.get(
    "/audit/duplicates",
    response_model=DuplicatesResponse,
    summary="Поиск дубликатов песен",
    description="Находит песни с одинаковым исполнителем и названием и схожей длительностью.",
)
async def get_library_duplicates(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> DuplicatesResponse:
    """Возвращает группы найденных дубликатов."""
    return await audit_service.find_duplicates()


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
    if path.strip() == "[object Object]":
        path = ""
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
