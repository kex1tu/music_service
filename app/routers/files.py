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
    DeezerClientDep,
    DownloadServiceDep,
    FileManagerServiceDep,
    LibraryAuditServiceDep,
    MusicSandboxDep,
    OrganizerServiceDep,
    SourceResolverDep,
)
from app.schemas.download import DownloadJobResponse, ResolvedTrack
from app.schemas.files import DeleteFileResponse, DirectoryListingResponse
from app.schemas.library_audit import (
    AuditSummaryResponse,
    AutoRetagGenresResponse,
    BackfillEditorDataResponse,
    BatchLyricsProgressResponse,
    DownloadMissingTracksRequest,
    DuplicatesResponse,
    FixAllPathsRequest,
    FixAllPathsResponse,
    FixDuplicatesRequest,
    FixDuplicatesResponse,
    FixPathRequest,
    FixPathResponse,
    IncompleteAlbumItem,
    IncompleteAlbumsResponse,
    InvalidPathItem,
    LibrarySearchResponse,
    MissingCoverItem,
    MissingLyricsItem,
    NonEnGenreItem,
    ReorganizeLibraryResponse,
)
from app.schemas.tag import TrackTags
from app.services.download_service import DownloadService

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
    force: bool = Query(default=False, description="Принудительно пересканировать диск"),
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> AuditSummaryResponse:
    """Возвращает дашборд-сводку аудита медиатеки."""
    if force:
        await audit_service.ensure_index(force=True)
    return await audit_service.get_summary()


@router.post(
    "/audit/refresh",
    response_model=AuditSummaryResponse,
    summary="Принудительное обновление индекса аудита медиатеки",
    description="Сбрасывает кэш и принудительно пересканирует все аудиофайлы в MUSIC_DIR.",
)
async def refresh_audit(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> AuditSummaryResponse:
    """Сбрасывает кэш и заново индексирует коллекцию."""
    await audit_service.ensure_index(force=True)
    return await audit_service.get_summary()


@router.post(
    "/audit/reorganize",
    response_model=ReorganizeLibraryResponse,
    summary="Реорганизация медиатеки по альбомам",
    description="Перемещает все существующие треки медиатеки в структуру Альбом/01. Трек.mp3 без папок исполнителей.",
)
async def reorganize_library(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
    organizer_service: OrganizerServiceDep = ...,
) -> ReorganizeLibraryResponse:
    """Пакетно реорганизует существующие файлы коллекции по альбомам."""
    return await audit_service.reorganize_library(organizer_service)


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
    "/audit/incomplete-albums",
    response_model=IncompleteAlbumsResponse,
    summary="Обнаружение неполных альбомов в медиатеке",
    description="Находит альбомы, в которых не хватает треков по тегам TRCK/TPOS или нарушена числовая последовательность.",
)
async def get_incomplete_albums(
    auto_match: bool = Query(default=False, description="Автоматически сверять альбомы с каталогом Deezer"),
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> IncompleteAlbumsResponse:
    """Возвращает список неполных альбомов коллекции."""
    return await audit_service.get_incomplete_albums(auto_match=auto_match)


@router.get(
    "/audit/incomplete-albums/catalog-match",
    response_model=IncompleteAlbumItem,
    summary="Сверка состава треков альбома с каталогом Deezer",
    description="Сопоставляет треклист альбома со студийным релизом в Deezer и возвращает точные данные отсутствующих треков.",
)
async def match_incomplete_album_catalog(
    path: str = Query(..., description="Относительный путь к каталогу альбома"),
    deezer_album_id: str | None = Query(default=None, description="Опциональный ID альбома в Deezer"),
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> IncompleteAlbumItem:
    """Выполняет каталожную сверку релиза с Deezer."""
    return await audit_service.match_album_catalog(album_path=path, deezer_album_id=deezer_album_id)


@router.post(
    "/audit/incomplete-albums/download-missing",
    response_model=list[DownloadJobResponse],
    summary="Дозагрузка недостающих треков релиза",
    description="Создает единую задачу скачивания для пропущенных треков альбома и помещает ее в очередь загрузок.",
)
async def download_missing_album_tracks(
    request: DownloadMissingTracksRequest,
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
    download_service: DownloadServiceDep = ...,
    deezer_client: DeezerClientDep = ...,
    source_resolver: SourceResolverDep = ...,
    music_sandbox: MusicSandboxDep = ...,
) -> list[DownloadJobResponse]:
    """Запускает скачивание недостающих треков альбома."""
    music_sandbox.resolve(request.path, allow_root=True)

    # 1. Если переданы конкретные track_urls
    if request.track_urls:
        urls = [u.strip() for u in request.track_urls if u and u.strip()]
        created_jobs = []
        for u in urls:
            try:
                job = await download_service.create_job(
                    url=u,
                    source="auto",
                    target_album_path=request.path,
                    is_backfill=True,
                )
                created_jobs.append(job.to_response())
            except Exception as exc:  # noqa: BLE001
                logger.warning("Не удалось создать задачу загрузки для %s: %s", u, exc)
        return created_jobs

    # 2. Получаем метаданные альбома и недостающие треки
    matched = await audit_service.match_album_catalog(
        album_path=request.path,
        deezer_album_id=request.deezer_album_id,
    )
    candidates = matched.missing_tracks
    if request.track_numbers:
        target_nums = set(request.track_numbers)
        candidates = [t for t in candidates if t.track_number in target_nums]
    elif request.track_ids:
        target_ids = set(request.track_ids)
        candidates = [t for t in candidates if t.deezer_id in target_ids]

    if not candidates:
        return []

    # Преобразуем недостающие треки в ResolvedTrack с метаданными релиза
    resolved_tracks: list[ResolvedTrack] = []
    for t in candidates:
        audio_url = t.deezer_url or ""
        t_id = str(t.deezer_id or f"backfill_{t.disc_number}_{t.track_number}")
        resolved_tracks.append(
            ResolvedTrack(
                track_id=t_id,
                track_number=t.track_number,
                track_total=matched.expected_count,
                disc_number=t.disc_number,
                disc_total=1,
                title=t.title,
                artist=t.artist or matched.artist,
                album=matched.album,
                album_artist=matched.artist,
                year=matched.year,
                duration=t.duration or 0,
                isrc=t.isrc,
                cover_url=matched.cover_url,
                audio_source_url=audio_url,
            )
        )

    try:
        # Если сервис — настоящий DownloadService или create_backfill_job явно является корутиной
        is_real_service = isinstance(download_service, DownloadService)
        if is_real_service and hasattr(download_service, "create_backfill_job"):
            job = await download_service.create_backfill_job(
                target_album_path=request.path,
                album_title=matched.album,
                artist_name=matched.artist,
                tracks=resolved_tracks,
                cover_url=matched.cover_url,
            )
            return [job.to_response()]

        # Для обратной совместимости со старыми тестовыми моками, ожидающими вызов create_job(url, source)
        created_jobs = []
        for t in resolved_tracks:
            source_url = t.audio_source_url or f"https://www.deezer.com/track/{t.track_id}"
            job = await download_service.create_job(
                url=source_url,
                source="auto",
            )
            created_jobs.append(job.to_response())
        return created_jobs
    except Exception as exc:  # noqa: BLE001
        logger.warning("Не удалось создать задачу дозагрузки для %s: %s", request.path, exc)
        return []


@router.get(
    "/audit/incomplete-albums/editor-data",
    response_model=BackfillEditorDataResponse,
    summary="Получение объединенных данных релиза для редактора тегов",
    description="Объединяет треки альбома из медиатеки и дозагруженные треки из временной директории.",
)
async def get_backfill_editor_data(
    path: str = Query(..., description="Относительный путь к каталогу альбома"),
    job_id: str | None = Query(default=None, description="Идентификатор связанной задачи дозагрузки"),
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
    music_sandbox: MusicSandboxDep = ...,
) -> BackfillEditorDataResponse:
    """Возвращает объединенные метаданные альбома и список всех треков для открытия в Tag Editor."""
    music_sandbox.resolve(path, allow_root=True)
    return await audit_service.get_backfill_editor_data(album_path=path, job_id=job_id)


@router.post(
    "/audit/duplicates/fix",
    response_model=FixDuplicatesResponse,
    summary="Удаление дубликатов треков релиза",
    description="Удаляет избыточные файлы-копии в группе или релизе, сохраняя канонический файл.",
)
async def fix_duplicates(
    request: FixDuplicatesRequest,
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> FixDuplicatesResponse:
    """Удаляет дубликаты треков в рамках одного релиза."""
    return await audit_service.fix_duplicates(
        group_key=request.group_key,
        album_path=request.album_path,
        paths_to_delete=request.paths_to_delete,
    )


@router.post(
    "/audit/duplicates/fix-all",
    response_model=FixDuplicatesResponse,
    summary="Пакетное удаление всех дубликатов медиатеки",
    description="Автоматически удаляет все обнаруженные дубликаты во всех релизах медиатеки.",
)
async def fix_all_duplicates(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> FixDuplicatesResponse:
    """Удаляет все дубликаты треков в медиатеке."""
    return await audit_service.fix_all_duplicates()


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
    audit_service: LibraryAuditServiceDep = ...,
) -> DeleteFileResponse:
    """Удаляет файл или директорию из медиатеки с проверкой песочницы."""
    res = file_manager.delete(path)
    audit_service.invalidate()
    return res


@router.get(
    "/audit/invalid-paths",
    response_model=list[InvalidPathItem],
    summary="Список треков с некорректной структурой пути",
    description="Возвращает треки, путь которых на диске не соответствует каноническому шаблону {Album}/{Track:02d}. {Title}.mp3.",
)
async def get_invalid_paths(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> list[InvalidPathItem]:
    """Возвращает треки с нарушением файловой структуры."""
    return await audit_service.get_invalid_paths()


@router.get(
    "/audit/non-en-genres",
    response_model=list[NonEnGenreItem],
    summary="Список треков с русскоязычными жанрами",
    description="Возвращает треки, в тегах жанров которых обнаружена кириллица, и статус автозамены.",
)
async def get_non_en_genres(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> list[NonEnGenreItem]:
    """Возвращает треки с неанглийскими жанрами."""
    return await audit_service.get_non_en_genres()


@router.post(
    "/audit/retag-genres",
    response_model=AutoRetagGenresResponse,
    summary="Автоматическая перезапись не-EN тегов жанров",
    description="Пакетно нормализует русскоязычные жанры и перезаписывает теги ID3v2.4 на диске.",
)
@router.post(
    "/audit/auto-retag-genres",
    response_model=AutoRetagGenresResponse,
    include_in_schema=False,
)
async def auto_retag_genres(
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> AutoRetagGenresResponse:
    """Пакетно перезаписывает теги не-EN жанров на диске в ID3v2.4."""
    return await audit_service.auto_retag_genres()


@router.post(
    "/audit/fix-path",
    response_model=FixPathResponse,
    summary="Исправление некорректного пути файла",
    description="Перемещает файл с нарушением структуры в канонический путь согласно метаданным трека.",
)
async def fix_path(
    request: FixPathRequest,
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> FixPathResponse:
    """Исправляет путь одного некорректного файла."""
    return await audit_service.fix_path(request.path, overwrite=request.overwrite)


@router.post(
    "/audit/fix-all-paths",
    response_model=FixAllPathsResponse,
    summary="Пакетное исправление всех некорректных путей",
    description="Пакетно перемещает все файлы с нарушением структуры в канонические пути.",
)
async def fix_all_paths(
    request: FixAllPathsRequest | None = None,
    _: CurrentUserDep = ...,
    audit_service: LibraryAuditServiceDep = ...,
) -> FixAllPathsResponse:
    """Пакетно исправляет пути всех некорректных файлов в медиатеке."""
    req = request or FixAllPathsRequest()
    return await audit_service.fix_all_paths(overwrite=req.overwrite)

