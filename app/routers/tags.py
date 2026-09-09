"""Маршруты API для чтения, обновления тегов и работы с обложками (Tags Router).

Предоставляет эндпоинты:
- GET /tags: получение тегов отдельного MP3-файла или агрегированных метаданных альбома.
- GET /tags/cover: получение бинарных данных фронтальной обложки (APIC type=3).
- PUT /tags/batch: пакетное сохранение тегов альбома с поддержкой двухуровневого редактирования.
Все эндпоинты защищены аутентификацией и песочницей PathSandbox.
"""

import base64
import logging
import uuid
from typing import Annotated

from fastapi import APIRouter, Depends, Response

from app.core.exceptions import NotFoundError, TagError
from app.dependencies import (
    CurrentUserDep,
    MusicSandboxDep,
    OrganizerServiceDep,
    TmpSandboxDep,
)
from app.schemas.tag import (
    AlbumDetailsResponse,
    AlbumTags,
    BatchSaveResponse,
    SaveAlbumRequest,
    TrackTags,
)
from app.services.tag_service import TagService

router = APIRouter(prefix="/tags", tags=["tags"])
logger = logging.getLogger(__name__)


def get_tag_service(
    music_sandbox: MusicSandboxDep,
    tmp_sandbox: TmpSandboxDep,
) -> TagService:
    """Фабрика зависимости сервиса TagService."""
    return TagService(music_sandbox=music_sandbox, tmp_sandbox=tmp_sandbox)


TagServiceDep = Annotated[TagService, Depends(get_tag_service)]


@router.get("", response_model=AlbumDetailsResponse | TrackTags)
def get_tags(
    path: str,
    _: CurrentUserDep,
    tag_service: TagServiceDep,
    as_album: bool = False,
) -> AlbumDetailsResponse | TrackTags:
    """Возвращает метаданные для указанного пути (файл трека или каталог альбома).

    Args:
        path: Относительный путь к файлу или каталогу в песочнице.
        _: Проверка аутентификации текущего пользователя.
        tag_service: Сервис работы с тегами.
        as_album: Принудительно упаковать одиночный трек в структуру AlbumDetailsResponse.

    Returns:
        TrackTags для одиночного MP3-файла (если as_album=False)
        или AlbumDetailsResponse для каталога / одиночного трека (при as_album=True).
    """
    resolved_path = tag_service.resolve_path(path, allow_root=False)
    if not resolved_path.exists():
        raise NotFoundError(f"Запрошенный путь не найден: {path}")

    if resolved_path.is_file():
        if resolved_path.suffix.lower() != ".mp3":
            raise TagError(f"Файл '{resolved_path.name}' не является поддерживаемым MP3-файлом")

        track_tags = tag_service.read_tags(resolved_path, rel_path=path)
        if as_album:
            album_tags = AlbumTags(
                album_artist=track_tags.album_artist or track_tags.artist,
                album=track_tags.album,
                year=track_tags.year,
                genre=track_tags.genre,
            )
            cover_info = tag_service.read_cover(resolved_path)
            return AlbumDetailsResponse(
                album=album_tags,
                tracks=[track_tags],
                has_cover=track_tags.has_cover or (cover_info is not None),
            )
        return track_tags

    return tag_service.read_album_details(path)


@router.get("/cover")
def get_cover(
    path: str,
    _: CurrentUserDep,
    tag_service: TagServiceDep,
) -> Response:
    """Возвращает бинарные данные фронтальной обложки альбома или трека.

    Args:
        path: Относительный путь к MP3-файлу или каталогу альбома.
        _: Проверка аутентификации текущего пользователя.
        tag_service: Сервис работы с тегами.

    Returns:
        Response с бинарными данными изображения и соответствующим MIME-типом.
    """
    resolved_path = tag_service.resolve_path(path, allow_root=False)
    if not resolved_path.exists():
        raise NotFoundError(f"Запрошенный путь не найден: {path}")

    cover_info = tag_service.read_cover(resolved_path)
    if not cover_info:
        raise NotFoundError("Обложка не найдена")

    cover_bytes, cover_mime = cover_info
    return Response(content=cover_bytes, media_type=cover_mime)


@router.put("/batch", response_model=BatchSaveResponse)
def batch_save_tags(
    request: SaveAlbumRequest,
    _: CurrentUserDep,
    tag_service: TagServiceDep,
    organizer_service: OrganizerServiceDep,
) -> BatchSaveResponse:
    """Пакетно сохраняет обновленные теги ID3v2.4 во все треки альбома и организует файлы.

    Args:
        request: Данные запроса на сохранение (альбом, список треков, флаг наследования, реорганизация).
        _: Проверка аутентификации текущего пользователя.
        tag_service: Сервис работы с тегами.
        organizer_service: Сервис организации и перемещения файлов медиатеки.

    Returns:
        BatchSaveResponse со статусом успешности, итоговым каталогом и списком сохраненных треков.
    """
    # 1. Проверяем наличие файла обложки (Base64 из браузера или cover_path)
    cover_bytes: bytes | None = None
    cover_mime = "image/jpeg"
    effective_cover_path = request.cover_path
    temp_upload_cover_abs = None

    if request.cover_base64:
        try:
            raw_b64 = request.cover_base64.strip()
            if "," in raw_b64:
                header, raw_b64 = raw_b64.split(",", 1)
                if "png" in header.lower():
                    cover_mime = "image/png"
                else:
                    cover_mime = "image/jpeg"
            cover_bytes = base64.b64decode(raw_b64)
            ext = ".png" if cover_mime == "image/png" else ".jpg"
            upload_cover_rel = f"tmp_downloads/uploads/{uuid.uuid4().hex}{ext}"
            upload_cover_abs = tag_service.resolve_path(upload_cover_rel)
            upload_cover_abs.parent.mkdir(parents=True, exist_ok=True)
            upload_cover_abs.write_bytes(cover_bytes)
            effective_cover_path = upload_cover_rel
            temp_upload_cover_abs = upload_cover_abs
        except (ValueError, OSError) as exc:
            logger.warning("Сбой декодирования загруженной обложки base64: %s", exc)
    elif request.cover_path:
        cover_file = tag_service.resolve_path(request.cover_path)
        if cover_file.exists() and cover_file.is_file():
            cover_bytes = cover_file.read_bytes()
            cover_mime = "image/png" if cover_file.suffix.lower() == ".png" else "image/jpeg"

    try:
        # Проверяем уникальность путей треков во избежание коллизий и сбоев перемещения
        track_paths = [t.path.strip() for t in request.tracks if t.path]
        if len(track_paths) != len(set(track_paths)):
            raise TagError("Список треков содержит дублирующиеся пути файлов")

        # 2. Применяем двухуровневые настройки альбома к трекам при необходимости
        if request.apply_album_to_tracks:
            tracks_to_process = tag_service.apply_album_defaults(request.tracks, request.album)
        else:
            tracks_to_process = [t.model_copy() for t in request.tracks]

        # 3. Сохраняем теги каждого трека через TagService
        saved_tracks: list[TrackTags] = []
        for track in tracks_to_process:
            resolved_file = tag_service.resolve_path(track.path)
            if not resolved_file.exists():
                raise NotFoundError(f"Аудиофайл не найден: {track.path}")

            track_to_save = track.model_copy()
            if cover_bytes is not None:
                track_to_save.has_cover = True

            tag_service.write_tags(
                file_path=resolved_file,
                tags=track_to_save,
                cover_bytes=cover_bytes,
                cover_mime=cover_mime,
            )

            saved_track = tag_service.read_tags(resolved_file, rel_path=track.path)
            saved_tracks.append(saved_track)

        # 4. Перемещение и структурирование файлов через OrganizerService
        org_result = organizer_service.organize_album(
            album=request.album,
            tracks=saved_tracks,
            source_dir=request.source_dir,
            cover_path=effective_cover_path,
            reorganize=request.reorganize,
        )

        return BatchSaveResponse(
            success=True,
            destination_dir=org_result.destination_dir,
            tracks=org_result.tracks,
        )
    finally:
        if temp_upload_cover_abs and temp_upload_cover_abs.exists():
            try:
                temp_upload_cover_abs.unlink(missing_ok=True)
                uploads_dir = temp_upload_cover_abs.parent
                if uploads_dir.exists() and not any(uploads_dir.iterdir()):
                    uploads_dir.rmdir()
            except OSError:
                pass
