"""Маршруты API для поиска, чтения и сохранения текстов песен (Lyrics Router).

Предоставляет эндпоинты:
- GET /lyrics/search — поиск текста в LRCLIB и Genius
- GET /lyrics — чтение текста существующего трека из файловой системы
- POST /lyrics — сохранение текста трека в виде внешнего файла .lrc / .txt
- DELETE /lyrics — удаление файлов текстов трека

Все эндпоинты защищены сессионной авторизацией get_current_user и изолированы
через PathSandbox.
"""

import logging
from typing import Annotated

from fastapi import APIRouter, Depends, Query

from app.dependencies import CurrentUserDep, MusicSandboxDep, SettingsDep, TmpSandboxDep
from app.schemas.lyrics import (
    DeleteLyricsResponse,
    LyricsResponse,
    LyricsSearchResult,
    SaveLyricsRequest,
)
from app.services.lyrics_service import GeniusClient, LrclibClient, LyricsService

logger = logging.getLogger("music_manager.routers.lyrics")

router = APIRouter(prefix="/lyrics", tags=["lyrics"])


def get_lrclib_client() -> LrclibClient:
    """Провайдер клиента LRCLIB."""
    return LrclibClient()


def get_genius_client(settings: SettingsDep) -> GeniusClient:
    """Провайдер клиента Genius API."""
    return GeniusClient(api_token=settings.GENIUS_API_TOKEN)


def get_lyrics_service(
    music_sandbox: MusicSandboxDep,
    tmp_sandbox: TmpSandboxDep,
    lrclib_client: Annotated[LrclibClient, Depends(get_lrclib_client)],
    genius_client: Annotated[GeniusClient, Depends(get_genius_client)],
) -> LyricsService:
    """Провайдер сервиса управления текстами песен LyricsService."""
    return LyricsService(
        music_sandbox=music_sandbox,
        tmp_sandbox=tmp_sandbox,
        lrclib_client=lrclib_client,
        genius_client=genius_client,
    )


LyricsServiceDep = Annotated[LyricsService, Depends(get_lyrics_service)]


@router.get(
    "/search",
    response_model=LyricsSearchResult,
    summary="Поиск текста песни во внешних провайдерах",
    description="Ищет текст трека в LRCLIB (синхронизированный/обычный) с фолбэком на Genius.",
)
async def search_lyrics(
    artist: str = Query(..., description="Исполнитель трека"),
    title: str = Query(..., description="Название трека"),
    album: str | None = Query(default=None, description="Название альбома"),
    duration: int | None = Query(default=None, ge=0, description="Длительность трека в секундах"),
    lyrics_service: LyricsServiceDep = ...,
    current_user: CurrentUserDep = ...,
) -> LyricsSearchResult:
    """Ищет текст трека через внешний каскад (LRCLIB synced -> LRCLIB plain -> Genius)."""
    logger.info("Пользователь '%s' ищет текст трека '%s - %s'", current_user, artist, title)
    return await lyrics_service.search(
        artist=artist,
        title=title,
        album=album,
        duration=duration,
    )


@router.get(
    "",
    response_model=LyricsResponse,
    summary="Получение текста трека из медиатеки",
    description="Считывает существующий файл текста (.lrc или .txt) рядом с целевым MP3 файлом.",
)
async def get_lyrics(
    path: str = Query(..., description="Относительный путь к целевому MP3-файлу"),
    lyrics_service: LyricsServiceDep = ...,
    current_user: CurrentUserDep = ...,
) -> LyricsResponse:
    """Возвращает существующий текст трека или статус none."""
    logger.debug("Пользователь '%s' запрашивает текст для: %s", current_user, path)
    return lyrics_service.get_lyrics_for_track(path)


@router.post(
    "",
    response_model=LyricsResponse,
    summary="Сохранение текста песни",
    description="Сохраняет переданный текст в отдельный файл .lrc или .txt рядом с MP3.",
)
async def save_lyrics(
    request: SaveLyricsRequest,
    lyrics_service: LyricsServiceDep = ...,
    current_user: CurrentUserDep = ...,
) -> LyricsResponse:
    """Сохраняет текст песни в файл на диске."""
    logger.info("Пользователь '%s' сохраняет текст для '%s' (kind=%s)", current_user, request.track_path, request.kind)
    lyrics_service.save_lyrics(
        rel_mp3_path=request.track_path,
        content=request.content,
        kind=request.kind,
    )
    return lyrics_service.get_lyrics_for_track(request.track_path)


@router.delete(
    "",
    response_model=DeleteLyricsResponse,
    summary="Удаление файла текста песни",
    description="Удаляет прилежащие к треку файлы .lrc и .txt с диска.",
)
async def delete_lyrics(
    path: str = Query(..., description="Относительный путь к целевому MP3-файлу"),
    lyrics_service: LyricsServiceDep = ...,
    current_user: CurrentUserDep = ...,
) -> DeleteLyricsResponse:
    """Удаляет файл текста трека."""
    logger.info("Пользователь '%s' запросил удаление текста для '%s'", current_user, path)
    deleted = lyrics_service.delete_lyrics(path)
    return DeleteLyricsResponse(success=True, deleted=deleted)
