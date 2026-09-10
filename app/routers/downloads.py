"""Маршруты API для управления загрузками (Downloads Router).

Предоставляет эндпоинты:
- POST /downloads — постановка задачи в очередь (HTTP 202 Accepted)
- POST /downloads/batch — пакетное создание задач
- POST /downloads/torrent — загрузка файла .torrent (multipart/form-data)
- GET /downloads/{job_id} — опрос статуса и прогресса (long-polling)
- GET /downloads/{job_id}/tracks — получение черновых метаданных треков (для Tag Editor)
- DELETE /downloads/{job_id} — отмена задачи и удаление временных файлов
Все эндпоинты защищены сессионной авторизацией get_current_user.
"""

import logging
import uuid
from typing import Annotated

from fastapi import APIRouter, File, UploadFile, status

from app.dependencies import CurrentUserDep, DownloadServiceDep, TmpSandboxDep
from app.schemas.download import (
    BatchDownloadResponse,
    CreateBatchDownloadRequest,
    CreateDownloadRequest,
    DownloadJobResponse,
    DownloadTracksResponse,
    QueueSummaryResponse,
)

logger = logging.getLogger("music_manager.routers.downloads")

router = APIRouter(prefix="/downloads", tags=["downloads"])


@router.post(
    "",
    response_model=DownloadJobResponse,
    status_code=status.HTTP_202_ACCEPTED,
    summary="Создание задачи загрузки",
    description="Принимает URL трека/альбома/плейлиста, валидирует источник и запускает фоновый процесс скачивания.",
)
async def create_download(
    request: CreateDownloadRequest,
    download_service: DownloadServiceDep,
    current_user: CurrentUserDep,
) -> DownloadJobResponse:
    """Создает задачу загрузки и возвращает ее статус QUEUED."""
    logger.info("Пользователь '%s' запросил загрузку URL: %s", current_user, request.url)
    job = await download_service.create_job(url=request.url, source=request.source)
    return job.to_response()


@router.post(
    "/batch",
    response_model=BatchDownloadResponse,
    status_code=status.HTTP_202_ACCEPTED,
    summary="Пакетное создание задач загрузки",
    description="Принимает список URL, валидирует источники и ставит пачку задач в очередь.",
)
async def create_batch_downloads(
    request: CreateBatchDownloadRequest,
    download_service: DownloadServiceDep,
    current_user: CurrentUserDep,
) -> BatchDownloadResponse:
    """Создает пачку задач загрузки и возвращает список их статусов QUEUED."""
    logger.info(
        "Пользователь '%s' запросил пакетную загрузку %d URL",
        current_user,
        len(request.urls),
    )
    jobs = await download_service.create_batch_jobs(
        urls=request.urls, source=request.source
    )
    return BatchDownloadResponse(
        job_ids=[j.id for j in jobs],
        jobs=[j.to_response() for j in jobs],
        total_queued=len(jobs),
    )


@router.post(
    "/torrent",
    response_model=DownloadJobResponse,
    status_code=status.HTTP_202_ACCEPTED,
    summary="Загрузка файла .torrent",
    description="Принимает файл .torrent через multipart/form-data, сохраняет во временный каталог и запускает загрузку.",
)
async def upload_torrent_file(
    download_service: DownloadServiceDep,
    current_user: CurrentUserDep,
    tmp_sandbox: TmpSandboxDep,
    file: Annotated[UploadFile, File(description="Файл .torrent")],
) -> DownloadJobResponse:
    """Создает задачу загрузки из загруженного файла .torrent."""
    if not file.filename or not file.filename.lower().endswith(".torrent"):
        from app.core.exceptions import UnsupportedSourceError

        raise UnsupportedSourceError("Допускаются только файлы с расширением .torrent")

    # Сохраняем .torrent файл во временный каталог
    torrent_id = uuid.uuid4().hex
    torrent_dir = tmp_sandbox.resolve(f"_torrents/{torrent_id}")
    torrent_dir.mkdir(parents=True, exist_ok=True)
    torrent_path = tmp_sandbox.resolve(f"_torrents/{torrent_id}/{file.filename}")

    content = await file.read()
    torrent_path.write_bytes(content)

    logger.info(
        "Пользователь '%s' загрузил .torrent файл: %s (%d байт)",
        current_user, file.filename, len(content),
    )

    job = await download_service.create_job(url=str(torrent_path), source="torrent")
    return job.to_response()


@router.get(
    "",
    response_model=list[DownloadJobResponse],
    summary="Получение списка всех задач в очереди",
    description="Возвращает список всех активных и завершенных задач в памяти.",
)
async def list_downloads(
    download_service: DownloadServiceDep,
    current_user: CurrentUserDep,
) -> list[DownloadJobResponse]:
    """Возвращает список всех задач загрузки."""
    return download_service.get_all_jobs()


@router.get(
    "/queue/summary",
    response_model=QueueSummaryResponse,
    summary="Получение агрегированного статуса очереди",
    description="Возвращает статистику для плавающего виджета очереди и прогресс-бара.",
)
async def get_queue_summary(
    download_service: DownloadServiceDep,
    current_user: CurrentUserDep,
) -> QueueSummaryResponse:
    """Возвращает сводку очереди загрузок."""
    return download_service.get_queue_summary()


@router.post(
    "/queue/clear",
    summary="Очистка завершенных и ошибочных задач из очереди",
    description="Удаляет из очереди задачи в статусах READY и ERROR.",
)
async def clear_completed_queue(
    download_service: DownloadServiceDep,
    current_user: CurrentUserDep,
) -> dict[str, int | str]:
    """Очищает завершенные задачи из очереди."""
    count = await download_service.clear_completed_jobs()
    logger.info("Пользователь '%s' очистил %d завершенных задач", current_user, count)
    return {"status": "cleared", "count": count}


@router.get(
    "/{job_id}",
    response_model=DownloadJobResponse,
    summary="Получение статуса задачи загрузки",
    description="Используется фронтендом для периодического опроса (long-polling каждые 1-2 сек).",
)
async def get_download_status(
    job_id: str,
    download_service: DownloadServiceDep,
    current_user: CurrentUserDep,
) -> DownloadJobResponse:
    """Возвращает текущее состояние задачи загрузки."""
    job = download_service.get_job(job_id)
    return job.to_response()


@router.get(
    "/{job_id}/tracks",
    response_model=DownloadTracksResponse,
    summary="Получение черновых метаданных треков",
    description="Возвращает список треков с предзаполненными метаданными после перехода задачи в статус READY.",
)
async def get_download_tracks(
    job_id: str,
    download_service: DownloadServiceDep,
    current_user: CurrentUserDep,
) -> DownloadTracksResponse:
    """Возвращает нормализованные треки релиза для предзаполнения Tag Editor."""
    return download_service.get_job_tracks(job_id)


@router.delete(
    "/{job_id}",
    summary="Отмена задачи и удаление временных файлов",
    description="Очищает каталог задачи в TMP_DIR и удаляет ее из реестра.",
)
async def cancel_download(
    job_id: str,
    download_service: DownloadServiceDep,
    current_user: CurrentUserDep,
) -> dict[str, str]:
    """Отменяет задачу и удаляет ее временные данные."""
    logger.info("Пользователь '%s' запросил удаление задачи %s", current_user, job_id)
    await download_service.cleanup_job(job_id)
    return {"status": "deleted", "job_id": job_id}
