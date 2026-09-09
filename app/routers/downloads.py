"""Маршруты API для управления загрузками (Downloads Router).

Предоставляет эндпоинты:
- POST /downloads — постановка задачи в очередь (HTTP 202 Accepted)
- GET /downloads/{job_id} — опрос статуса и прогресса (long-polling)
- GET /downloads/{job_id}/tracks — получение черновых метаданных треков (для Tag Editor)
- DELETE /downloads/{job_id} — отмена задачи и удаление временных файлов
Все эндпоинты защищены сессионной авторизацией get_current_user.
"""

import logging

from fastapi import APIRouter, status

from app.dependencies import CurrentUserDep, DownloadServiceDep
from app.schemas.download import (
    CreateDownloadRequest,
    DownloadJobResponse,
    DownloadTracksResponse,
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
