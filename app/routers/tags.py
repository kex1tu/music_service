"""Маршруты API для чтения, обновления тегов и работы с обложками (Tags Router).

Предоставляет эндпоинты:
- GET /tags: получение тегов отдельного MP3-файла или агрегированных метаданных альбома.
- GET /tags/cover: получение бинарных данных фронтальной обложки (APIC type=3).
- PUT /tags/batch: пакетное сохранение тегов альбома с поддержкой двухуровневого редактирования.
Все эндпоинты защищены аутентификацией и песочницей PathSandbox.
"""

import base64
import logging
import re
import uuid
from typing import Annotated

from fastapi import APIRouter, Depends, Query, Response

from app.core.exceptions import NotFoundError, PathTraversalError, TagError
from app.dependencies import (
    CurrentUserDep,
    LibraryAuditServiceDep,
    MusicSandboxDep,
    OrganizerServiceDep,
    TmpSandboxDep,
)
from app.schemas.tag import (
    AlbumDetailsResponse,
    AlbumTags,
    BatchSaveResponse,
    CoverSearchResultItem,
    DeezerAlbumDetailsResponse,
    DeezerAlbumSearchResult,
    DeezerAlbumTrackItem,
    RecognizeAudioRequest,
    RecognizedTrackResponse,
    SaveAlbumRequest,
    TrackTags,
)
from app.services.deezer_client import DeezerClient
from app.services.recognition_service import RecognitionService
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


def get_recognition_service(
    music_sandbox: MusicSandboxDep,
    tmp_sandbox: TmpSandboxDep,
) -> RecognitionService:
    """Фабрика зависимости сервиса RecognitionService."""
    return RecognitionService(music_sandbox=music_sandbox, tmp_sandbox=tmp_sandbox)


RecognitionServiceDep = Annotated[RecognitionService, Depends(get_recognition_service)]


def get_deezer_client() -> DeezerClient:
    """Фабрика зависимости клиента DeezerClient."""
    return DeezerClient()


DeezerClientDep = Annotated[DeezerClient, Depends(get_deezer_client)]


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


@router.get("/covers/search", response_model=list[CoverSearchResultItem])
async def search_covers(
    query: str = Query(..., min_length=1, description="Поисковый запрос (альбом или исполнитель)"),
    limit: int = Query(default=12, ge=1, le=30),
    _: CurrentUserDep = ...,
    tag_service: TagServiceDep = ...,
) -> list[CoverSearchResultItem]:
    """Поиск обложек альбомов высокого разрешения в публичных сервисах."""
    return await tag_service.search_covers(query=query, limit=limit)


@router.put("/batch", response_model=BatchSaveResponse)
async def batch_save_tags(
    request: SaveAlbumRequest,
    _: CurrentUserDep,
    tag_service: TagServiceDep,
    organizer_service: OrganizerServiceDep,
    audit_service: LibraryAuditServiceDep,
) -> BatchSaveResponse:
    """Пакетно сохраняет обновленные теги ID3v2.4 во все треки альбома и организует файлы.

    Args:
        request: Данные запроса на сохранение (альбом, список треков, флаг наследования, реорганизация).
        _: Проверка аутентификации текущего пользователя.
        tag_service: Сервис работы с тегами.
        organizer_service: Сервис организации и перемещения файлов медиатеки.
        audit_service: Сервис аудита медиатеки для сброса in-memory кэша.

    Returns:
        BatchSaveResponse со статусом успешности, итоговым каталогом и списком сохраненных треков.
    """
    # 1. Проверяем наличие файла обложки (Base64 из браузера, внешний cover_url или cover_path)
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
    elif request.cover_url:
        try:
            dl_bytes, dl_mime = await tag_service.download_cover(request.cover_url)
            cover_bytes = dl_bytes
            cover_mime = dl_mime
            ext = ".png" if cover_mime == "image/png" else ".jpg"
            upload_cover_rel = f"tmp_downloads/uploads/{uuid.uuid4().hex}{ext}"
            upload_cover_abs = tag_service.resolve_path(upload_cover_rel)
            upload_cover_abs.parent.mkdir(parents=True, exist_ok=True)
            upload_cover_abs.write_bytes(cover_bytes)
            effective_cover_path = upload_cover_rel
            temp_upload_cover_abs = upload_cover_abs
        except Exception as exc:  # noqa: BLE001
            logger.warning("Сбой скачивания выбранной обложки по URL '%s': %s", request.cover_url, exc)
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
        track_covers_data: dict[str, bytes] = {}

        for track in tracks_to_process:
            resolved_file = tag_service.resolve_path(track.path)
            if not resolved_file.exists():
                raise NotFoundError(f"Аудиофайл не найден: {track.path}")

            track_cover_bytes: bytes | None = None
            track_cover_mime = "image/jpeg"

            if track.cover_base64:
                try:
                    raw_b64 = track.cover_base64.strip()
                    if "," in raw_b64:
                        header, raw_b64 = raw_b64.split(",", 1)
                        if "png" in header.lower():
                            track_cover_mime = "image/png"
                        else:
                            track_cover_mime = "image/jpeg"
                    track_cover_bytes = base64.b64decode(raw_b64)
                except (ValueError, OSError) as exc:
                    logger.warning("Сбой декодирования обложки трека %s base64: %s", track.path, exc)
            elif track.cover_url:
                try:
                    dl_bytes, dl_mime = await tag_service.download_cover(track.cover_url)
                    track_cover_bytes = dl_bytes
                    track_cover_mime = dl_mime
                except Exception as exc:  # noqa: BLE001
                    logger.warning("Сбой скачивания обложки трека %s по URL '%s': %s", track.path, track.cover_url, exc)
            elif track.cover_path:
                try:
                    c_file = tag_service.resolve_path(track.cover_path)
                    if c_file.exists() and c_file.is_file():
                        track_cover_bytes = c_file.read_bytes()
                        track_cover_mime = "image/png" if c_file.suffix.lower() == ".png" else "image/jpeg"
                except (PathTraversalError, OSError) as exc:
                    logger.warning("Сбой чтения обложки трека %s из %s: %s", track.path, track.cover_path, exc)

            if track_cover_bytes is None:
                track_cover_bytes = cover_bytes
                track_cover_mime = cover_mime

            if track_cover_bytes is not None:
                track_covers_data[track.path] = track_cover_bytes

            track_to_save = track.model_copy()
            if track_cover_bytes is not None:
                track_to_save.has_cover = True

            tag_service.write_tags(
                file_path=resolved_file,
                tags=track_to_save,
                cover_bytes=track_cover_bytes,
                cover_mime=track_cover_mime,
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
            singles_mode=request.singles_mode,
            group_by_album=request.group_by_album,
            track_covers_data=track_covers_data,
        )

        # Сбрасываем кэш аудита для мгновенного отражения изменений
        audit_service.invalidate()

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


@router.post("/recognize-audio", response_model=RecognizedTrackResponse)
async def recognize_audio(
    request: RecognizeAudioRequest,
    _: CurrentUserDep,
    recognition_service: RecognitionServiceDep,
) -> RecognizedTrackResponse:
    """Распознает трек по его аудиосодержимому через Shazam."""
    return await recognition_service.recognize_audio(request.path)


@router.get("/deezer/albums", response_model=list[DeezerAlbumSearchResult])
async def search_deezer_albums(
    query: str = Query(..., description="Поисковый запрос (название альбома или артист)"),
    _: CurrentUserDep = ...,
    deezer_client: DeezerClientDep = ...,
    limit: int = Query(default=6, ge=1, le=20),
) -> list[DeezerAlbumSearchResult]:
    """Поиск альбомов в публичном каталоге Deezer."""
    items = await deezer_client.search_albums(query=query, limit=limit)
    results: list[DeezerAlbumSearchResult] = []
    for item in items:
        artist_name = deezer_client.format_artists(item)
        cover_url = item.get("cover_xl") or item.get("cover_big") or item.get("cover_medium") or item.get("cover")
        results.append(
            DeezerAlbumSearchResult(
                id=str(item.get("id")),
                title=item.get("title", ""),
                artist=artist_name,
                cover_url=cover_url,
                nb_tracks=item.get("nb_tracks", 0),
                year=None,
            )
        )
    return results


@router.get("/deezer/albums/{album_id}", response_model=DeezerAlbumDetailsResponse)
async def get_deezer_album_details(
    album_id: str,
    _: CurrentUserDep,
    deezer_client: DeezerClientDep,
) -> DeezerAlbumDetailsResponse:
    """Получение официальных метаданных альбома, обложки и треклиста из Deezer."""
    data = await deezer_client.get_album(album_id, fetch_track_details=True)
    artist_name = deezer_client.format_artists(data)

    release_date = data.get("release_date")
    year: int | None = None
    if release_date:
        m = re.match(r"^(\d{4})", str(release_date))
        if m:
            year = int(m.group(1))

    genre: str | None = None
    genres_data = data.get("genres", {})
    genres_list = genres_data.get("data", []) if isinstance(genres_data, dict) else []
    if genres_list and isinstance(genres_list, list):
        genre_names = [g.get("name") for g in genres_list if isinstance(g, dict) and g.get("name")]
        if genre_names:
            genre = "\\\\".join(genre_names)

    cover_url = deezer_client.extract_best_cover_url(data)

    tracks_data = data.get("tracks", {}).get("data", [])
    tracks_items: list[DeezerAlbumTrackItem] = []
    for idx, t in enumerate(tracks_data):
        track_num = t.get("track_position") or (idx + 1)
        t_artist = deezer_client.format_artists(t)
        if not t_artist or t_artist == "Unknown Artist":
            t_artist = artist_name
        tracks_items.append(
            DeezerAlbumTrackItem(
                track_number=int(track_num),
                title=t.get("title", ""),
                artist=t_artist,
                duration=t.get("duration"),
            )
        )

    return DeezerAlbumDetailsResponse(
        id=str(data.get("id")),
        title=data.get("title", ""),
        artist=artist_name,
        year=year,
        genre=genre,
        cover_url=cover_url,
        tracks=tracks_items,
    )


