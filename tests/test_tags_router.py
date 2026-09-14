"""Интеграционные тесты для маршрутов тегов (GET /tags, GET /tags/cover, PUT /tags/batch)."""

from collections.abc import Generator
from pathlib import Path

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from app.config import Settings
from app.core.sandbox import PathSandbox
from app.main import create_app
from app.routers.tags import router as tags_router
from app.schemas.tag import TrackTags
from app.services.tag_service import TagService

MINIMAL_MP3 = b"\xff\xfb\x90\x00" + b"\x00" * 300
DUMMY_JPEG = b"\xff\xd8\xff\xe0\x00\x10JFIF" + b"\x00" * 64
DUMMY_PNG = b"\x89PNG\r\n\x1a\n" + b"\x00" * 64


@pytest.fixture
def tags_app(test_settings: Settings) -> FastAPI:
    """Создает приложение FastAPI с зарегистрированным роутером тегов."""
    app = create_app(test_settings)
    app.include_router(tags_router)
    return app


@pytest.fixture
def tags_client(tags_app: FastAPI) -> Generator[TestClient, None, None]:
    """Неавторизованный TestClient для проверки защиты маршрутов тегов."""
    with TestClient(tags_app) as tc:
        yield tc


@pytest.fixture
def auth_tags_client(
    tags_app: FastAPI, test_settings: Settings, session_token: str
) -> Generator[TestClient, None, None]:
    """Авторизованный TestClient с валидной сессией для маршрутов тегов."""
    with TestClient(tags_app, cookies={test_settings.SESSION_COOKIE_NAME: session_token}) as tc:
        yield tc


def test_tags_router_unauthorized(tags_client: TestClient):
    """Проверка запрета доступа к эндпоинтам тегов без аутентификации (HTTP 401)."""
    resp_get = tags_client.get("/tags", params={"path": "some_track.mp3"})
    assert resp_get.status_code == 401
    assert resp_get.json()["error"] == "UNAUTHORIZED"

    resp_cover = tags_client.get("/tags/cover", params={"path": "some_track.mp3"})
    assert resp_cover.status_code == 401
    assert resp_cover.json()["error"] == "UNAUTHORIZED"

    resp_batch = tags_client.put(
        "/tags/batch",
        json={
            "album": {"album": "A", "album_artist": "B"},
            "tracks": [],
            "source_dir": "some_dir",
        },
    )
    assert resp_batch.status_code == 401
    assert resp_batch.json()["error"] == "UNAUTHORIZED"


def test_get_tags_single_track(auth_tags_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка GET /tags для одиночного MP3-файла (TrackTags и as_album=True)."""
    music_dir = temp_storage["music_dir"]
    track_file = music_dir / "01. Around the World.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    svc = TagService(music_sandbox=PathSandbox(music_dir))
    svc.write_tags(
        track_file,
        TrackTags(
            path="01. Around the World.mp3",
            track_number=1,
            title="Around the World",
            artist="Daft Punk",
            album="Homework",
            album_artist="Daft Punk",
            year=1997,
            genre="House",
        ),
    )

    # 1. По умолчанию: возвращает TrackTags
    resp = auth_tags_client.get("/tags", params={"path": "01. Around the World.mp3"})
    assert resp.status_code == 200
    data = resp.json()
    assert data["title"] == "Around the World"
    assert data["artist"] == "Daft Punk"
    assert data["album"] == "Homework"
    assert data["year"] == 1997
    assert data["genre"] == "House"

    # 2. С параметром as_album=True: возвращает AlbumDetailsResponse
    resp_album = auth_tags_client.get(
        "/tags",
        params={"path": "01. Around the World.mp3", "as_album": "true"},
    )
    assert resp_album.status_code == 200
    album_data = resp_album.json()
    assert "album" in album_data
    assert album_data["album"]["album"] == "Homework"
    assert len(album_data["tracks"]) == 1
    assert album_data["tracks"][0]["title"] == "Around the World"


def test_get_tags_directory(auth_tags_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка GET /tags для каталога альбома (AlbumDetailsResponse со списком треков)."""
    music_dir = temp_storage["music_dir"]
    album_dir = music_dir / "Daft Punk" / "Discovery"
    album_dir.mkdir(parents=True, exist_ok=True)

    t1 = album_dir / "02. Aerodynamic.mp3"
    t2 = album_dir / "01. One More Time.mp3"
    t1.write_bytes(MINIMAL_MP3)
    t2.write_bytes(MINIMAL_MP3)

    svc = TagService(music_sandbox=PathSandbox(music_dir))
    svc.write_tags(
        t1,
        TrackTags(
            path="Daft Punk/Discovery/02. Aerodynamic.mp3",
            track_number=2,
            title="Aerodynamic",
            artist="Daft Punk",
            album="Discovery",
            album_artist="Daft Punk",
            year=2001,
        ),
    )
    svc.write_tags(
        t2,
        TrackTags(
            path="Daft Punk/Discovery/01. One More Time.mp3",
            track_number=1,
            title="One More Time",
            artist="Daft Punk\\\\Romanthony",
            album="Discovery",
            album_artist="Daft Punk",
            year=2001,
        ),
    )

    resp = auth_tags_client.get("/tags", params={"path": "Daft Punk/Discovery"})
    assert resp.status_code == 200
    data = resp.json()

    assert data["album"]["album"] == "Discovery"
    assert data["album"]["album_artist"] == "Daft Punk"
    assert data["album"]["year"] == 2001
    assert len(data["tracks"]) == 2
    # Проверка сортировки по track_number
    assert data["tracks"][0]["track_number"] == 1
    assert data["tracks"][0]["title"] == "One More Time"
    assert data["tracks"][1]["track_number"] == 2
    assert data["tracks"][1]["title"] == "Aerodynamic"


def test_get_tags_cover_embedded(auth_tags_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка GET /tags/cover для файла со встроенной обложкой (APIC type=3)."""
    music_dir = temp_storage["music_dir"]
    track_file = music_dir / "covered.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    svc = TagService(music_sandbox=PathSandbox(music_dir))
    svc.write_tags(
        track_file,
        TrackTags(path="covered.mp3", title="Covered Song"),
        cover_bytes=DUMMY_JPEG,
        cover_mime="image/jpeg",
    )

    resp = auth_tags_client.get("/tags/cover", params={"path": "covered.mp3"})
    assert resp.status_code == 200
    assert resp.headers["content-type"] == "image/jpeg"
    assert resp.content == DUMMY_JPEG


def test_get_tags_cover_from_directory(auth_tags_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка GET /tags/cover для каталога с файлом обложки (cover.png)."""
    music_dir = temp_storage["music_dir"]
    album_dir = music_dir / "FolderAlbum"
    album_dir.mkdir(parents=True, exist_ok=True)

    cover_file = album_dir / "cover.png"
    cover_file.write_bytes(DUMMY_PNG)

    resp = auth_tags_client.get("/tags/cover", params={"path": "FolderAlbum"})
    assert resp.status_code == 200
    assert resp.headers["content-type"] == "image/png"
    assert resp.content == DUMMY_PNG


def test_get_tags_cover_not_found(auth_tags_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка GET /tags/cover при отсутствии обложки (HTTP 404)."""
    music_dir = temp_storage["music_dir"]
    track_file = music_dir / "nocover.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    resp = auth_tags_client.get("/tags/cover", params={"path": "nocover.mp3"})
    assert resp.status_code == 404
    assert resp.json()["error"] == "RESOURCE_NOT_FOUND"


def test_batch_save_tags(auth_tags_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка PUT /tags/batch: обновление тегов на диске и двухуровневое наследование."""
    music_dir = temp_storage["music_dir"]
    album_dir = music_dir / "Modjo" / "Modjo"
    album_dir.mkdir(parents=True, exist_ok=True)

    t1 = album_dir / "01. Lady.mp3"
    t2 = album_dir / "02. Chillin.mp3"
    t1.write_bytes(MINIMAL_MP3)
    t2.write_bytes(MINIMAL_MP3)

    payload = {
        "album": {
            "album": "Modjo (Remastered)",
            "album_artist": "Modjo",
            "year": 2001,
            "genre": "French Touch\\\\Disco",
        },
        "tracks": [
            {
                "path": "Modjo/Modjo/01. Lady.mp3",
                "track_number": 1,
                "title": "Lady (Hear Me Tonight)",
                "artist": "Modjo",
            },
            {
                "path": "Modjo/Modjo/02. Chillin.mp3",
                "track_number": 2,
                "title": "Chillin'",
                "artist": "Modjo",
            },
        ],
        "source_dir": "Modjo/Modjo",
        "apply_album_to_tracks": True,
    }

    resp = auth_tags_client.put("/tags/batch", json=payload)
    assert resp.status_code == 200
    data = resp.json()
    assert data["success"] is True
    assert data["destination_dir"] == "Modjo/Modjo"
    assert len(data["tracks"]) == 2
    assert data["tracks"][0]["album"] == "Modjo (Remastered)"
    assert data["tracks"][0]["year"] == 2001
    assert data["tracks"][0]["genre"] == "French Touch\\\\Disco"

    # Проверяем, что теги действительно сохранились на диске в формате ID3v2.4
    svc = TagService(music_sandbox=PathSandbox(music_dir))
    disk_track1 = svc.read_tags(t1, rel_path="Modjo/Modjo/01. Lady.mp3")
    assert disk_track1.title == "Lady (Hear Me Tonight)"
    assert disk_track1.album == "Modjo (Remastered)"
    assert disk_track1.genre == "French Touch\\\\Disco"
    assert disk_track1.year == 2001

    disk_track2 = svc.read_tags(t2, rel_path="Modjo/Modjo/02. Chillin.mp3")
    assert disk_track2.title == "Chillin'"
    assert disk_track2.album == "Modjo (Remastered)"


def test_batch_save_with_cover(auth_tags_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка PUT /tags/batch со внедрением новой обложки во все треки."""
    music_dir = temp_storage["music_dir"]
    album_dir = music_dir / "Kavinsky" / "OutRun"
    album_dir.mkdir(parents=True, exist_ok=True)

    t1 = album_dir / "01. Nightcall.mp3"
    t1.write_bytes(MINIMAL_MP3)

    cover_file = album_dir / "new_cover.jpg"
    cover_file.write_bytes(DUMMY_JPEG)

    payload = {
        "album": {
            "album": "OutRun",
            "album_artist": "Kavinsky",
            "year": 2013,
            "genre": "Synthwave",
        },
        "tracks": [
            {
                "path": "Kavinsky/OutRun/01. Nightcall.mp3",
                "track_number": 1,
                "title": "Nightcall",
                "artist": "Kavinsky\\\\Lovefoxxx",
            }
        ],
        "source_dir": "Kavinsky/OutRun",
        "cover_path": "Kavinsky/OutRun/new_cover.jpg",
        "apply_album_to_tracks": True,
    }

    resp = auth_tags_client.put("/tags/batch", json=payload)
    assert resp.status_code == 200

    svc = TagService(music_sandbox=PathSandbox(music_dir))
    cover_info = svc.read_cover(t1)
    assert cover_info is not None
    assert cover_info[0] == DUMMY_JPEG
    assert cover_info[1] == "image/jpeg"


def test_tags_path_traversal_returns_403(auth_tags_client: TestClient):
    """Инвариант: любые попытки Path Traversal возвращают HTTP 403."""
    # GET /tags
    resp_get = auth_tags_client.get("/tags", params={"path": "../../etc/passwd"})
    assert resp_get.status_code == 403
    assert resp_get.json()["error"] == "PATH_TRAVERSAL_DETECTED"

    # GET /tags/cover
    resp_cover = auth_tags_client.get("/tags/cover", params={"path": "../../etc/shadow"})
    assert resp_cover.status_code == 403
    assert resp_cover.json()["error"] == "PATH_TRAVERSAL_DETECTED"

    # PUT /tags/batch
    resp_batch = auth_tags_client.put(
        "/tags/batch",
        json={
            "album": {"album": "A", "album_artist": "B"},
            "tracks": [{"path": "../../etc/passwd", "track_number": 1, "title": "Evil"}],
            "source_dir": "normal_dir",
        },
    )
    assert resp_batch.status_code == 403
    assert resp_batch.json()["error"] == "PATH_TRAVERSAL_DETECTED"


def test_tags_not_found_returns_404(auth_tags_client: TestClient):
    """Проверка возврата HTTP 404 при обращении к несуществующему ресурсу."""
    resp_get = auth_tags_client.get("/tags", params={"path": "missing_directory/track.mp3"})
    assert resp_get.status_code == 404
    assert resp_get.json()["error"] == "RESOURCE_NOT_FOUND"

    resp_cover = auth_tags_client.get("/tags/cover", params={"path": "missing_directory"})
    assert resp_cover.status_code == 404
    assert resp_cover.json()["error"] == "RESOURCE_NOT_FOUND"


def test_get_tags_invalid_file_type_returns_422(
    auth_tags_client: TestClient, temp_storage: dict[str, Path]
):
    """Проверка ошибки валидации типа файла при запросе тегов не-MP3 файла."""
    music_dir = temp_storage["music_dir"]
    txt_file = music_dir / "readme.txt"
    txt_file.write_text("just text", encoding="utf-8")

    resp = auth_tags_client.get("/tags", params={"path": "readme.txt"})
    assert resp.status_code == 422
    assert resp.json()["error"] == "TAG_PROCESSING_ERROR"


def test_batch_save_from_tmp_moves_to_music_dir(
    auth_tags_client: TestClient, temp_storage: dict[str, Path]
):
    """Проверка PUT /tags/batch: перемещение из TMP_DIR в MUSIC_DIR по структуре."""
    tmp_dir = temp_storage["tmp_dir"]
    music_dir = temp_storage["music_dir"]

    job_id = "job_batch_tmp"
    conv_dir = tmp_dir / job_id / "converted"
    conv_dir.mkdir(parents=True, exist_ok=True)

    t1 = conv_dir / "01. draft.mp3"
    t1.write_bytes(MINIMAL_MP3)
    l1 = conv_dir / "01. draft.lrc"
    l1.write_text("[00:01.00] test lyrics", encoding="utf-8")

    cover = tmp_dir / job_id / "cover.jpg"
    cover.write_bytes(DUMMY_JPEG)

    payload = {
        "album": {
            "album": "Discovery",
            "album_artist": "Daft Punk",
            "year": 2001,
            "genre": "House",
        },
        "tracks": [
            {
                "path": f"{job_id}/converted/01. draft.mp3",
                "track_number": 1,
                "title": "One More Time",
                "artist": "Daft Punk",
            }
        ],
        "source_dir": f"{job_id}/converted",
        "cover_path": f"{job_id}/cover.jpg",
        "apply_album_to_tracks": True,
    }

    resp = auth_tags_client.put("/tags/batch", json=payload)
    assert resp.status_code == 200
    data = resp.json()
    assert data["success"] is True
    expected_album_dir = "Daft Punk/Discovery - 2001"
    assert data["destination_dir"] == expected_album_dir
    assert len(data["tracks"]) == 1
    assert data["tracks"][0]["path"] == f"{expected_album_dir}/01. One More Time.mp3"

    # Файлы должны переместиться в MUSIC_DIR
    final_mp3 = music_dir / "Daft Punk" / "Discovery - 2001" / "01. One More Time.mp3"
    final_lrc = music_dir / "Daft Punk" / "Discovery - 2001" / "01. One More Time.lrc"
    final_cover = music_dir / "Daft Punk" / "Discovery - 2001" / "cover.jpg"

    assert final_mp3.exists()
    assert final_lrc.exists()
    assert final_cover.exists()

    # Временный каталог должен быть очищен
    assert not (tmp_dir / job_id).exists()


def test_batch_save_with_reorganize_true(
    auth_tags_client: TestClient, temp_storage: dict[str, Path]
):
    """Проверка PUT /tags/batch с флагом reorganize=True: перемещение и очистка старых папок."""
    music_dir = temp_storage["music_dir"]

    old_dir = music_dir / "OldBand" / "OldRecord - 2000"
    old_dir.mkdir(parents=True, exist_ok=True)
    old_file = old_dir / "01. Track.mp3"
    old_file.write_bytes(MINIMAL_MP3)

    payload = {
        "album": {
            "album": "NewRecord",
            "album_artist": "NewBand",
            "year": 2025,
            "genre": "Rock",
        },
        "tracks": [
            {
                "path": "OldBand/OldRecord - 2000/01. Track.mp3",
                "track_number": 3,
                "title": "Renamed Track",
                "artist": "NewBand",
            }
        ],
        "source_dir": "OldBand/OldRecord - 2000",
        "apply_album_to_tracks": True,
        "reorganize": True,
    }

    resp = auth_tags_client.put("/tags/batch", json=payload)
    assert resp.status_code == 200
    data = resp.json()
    assert data["success"] is True
    expected_dir = "NewBand/NewRecord - 2025"
    assert data["destination_dir"] == expected_dir
    assert data["tracks"][0]["path"] == f"{expected_dir}/03. Renamed Track.mp3"

    # Файл должен переместиться
    new_file = music_dir / "NewBand" / "NewRecord - 2025" / "03. Renamed Track.mp3"
    assert new_file.exists()

    # Старые пустые директории должны быть удалены
    assert not old_dir.exists()
    assert not (music_dir / "OldBand").exists()


def test_batch_save_with_cover_base64_upload(
    auth_tags_client: TestClient, temp_storage: dict[str, Path]
):
    """Проверка загрузки обложки через cover_base64 в PUT /tags/batch."""
    import base64

    music_dir = temp_storage["music_dir"]
    track_file = music_dir / "01. Song.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    b64_data = "data:image/jpeg;base64," + base64.b64encode(DUMMY_JPEG).decode("ascii")

    payload = {
        "album": {
            "album": "Base64Album",
            "album_artist": "Base64Artist",
            "year": 2026,
            "genre": "Electronic",
        },
        "tracks": [
            {
                "path": "01. Song.mp3",
                "track_number": 1,
                "title": "Song",
                "artist": "Base64Artist",
            }
        ],
        "source_dir": "",
        "cover_base64": b64_data,
        "apply_album_to_tracks": True,
        "reorganize": True,
    }

    resp = auth_tags_client.put("/tags/batch", json=payload)
    assert resp.status_code == 200
    data = resp.json()
    assert data["success"] is True

    # Проверяем, что обложка сохранена как cover.jpg в папке альбома
    target_dir = music_dir / data["destination_dir"]
    cover_file = target_dir / "cover.jpg"
    assert cover_file.exists()
    assert cover_file.read_bytes() == DUMMY_JPEG

    # Проверяем, что обложка вшита в теги MP3
    svc = TagService(music_sandbox=PathSandbox(music_dir))
    mp3_file = target_dir / "01. Song.mp3"
    cover_info = svc.read_cover(mp3_file)
    assert cover_info is not None
    assert cover_info[0] == DUMMY_JPEG

    # Проверяем отсутствие утечек временных файлов обложек в TMP_DIR
    tmp_uploads = temp_storage["tmp_dir"] / "uploads"
    if tmp_uploads.exists():
        assert len(list(tmp_uploads.glob("*"))) == 0


def test_recognize_audio_endpoints(tags_client: TestClient, auth_tags_client: TestClient, temp_storage: dict):
    """Проверка работы эндпоинта POST /tags/recognize-audio."""
    # 1. 401 Unauthorized
    resp_unauth = tags_client.post("/tags/recognize-audio", json={"path": "track.mp3"})
    assert resp_unauth.status_code == 401

    # 2. Успешное распознавание с моком
    from unittest.mock import AsyncMock, patch

    from app.schemas.tag import RecognizedTrackResponse
    from app.services.recognition_service import RecognitionService

    expected = RecognizedTrackResponse(
        recognized=True,
        title="Around the World",
        artist="Daft Punk",
        album="Homework",
        year=1997,
        genre="Electronic",
        cover_url="https://is1-ssl.mzstatic.com/image/thumb/1000x1000cc.jpg",
    )

    with patch.object(RecognitionService, "recognize_audio", new=AsyncMock(return_value=expected)):
        resp = auth_tags_client.post("/tags/recognize-audio", json={"path": "track.mp3"})
        assert resp.status_code == 200
        data = resp.json()
        assert data["recognized"] is True
        assert data["title"] == "Around the World"
        assert data["artist"] == "Daft Punk"
        assert data["album"] == "Homework"
        assert data["year"] == 1997


def test_deezer_album_endpoints(tags_client: TestClient, auth_tags_client: TestClient):
    """Проверка эндпоинтов поиска и деталей альбома в Deezer."""
    # 1. 401 Unauthorized
    resp_search_unauth = tags_client.get("/tags/deezer/albums", params={"query": "Discovery"})
    assert resp_search_unauth.status_code == 401

    resp_details_unauth = tags_client.get("/tags/deezer/albums/302127")
    assert resp_details_unauth.status_code == 401

    # 2. Поиск альбомов с моком
    from unittest.mock import AsyncMock, patch

    from app.services.deezer_client import DeezerClient

    mock_search = [
        {
            "id": 302127,
            "title": "Discovery",
            "artist": {"name": "Daft Punk"},
            "cover_xl": "https://cdn-images.dzcdn.net/cover.jpg",
            "nb_tracks": 14,
        }
    ]

    with patch.object(DeezerClient, "search_albums", new=AsyncMock(return_value=mock_search)):
        resp = auth_tags_client.get("/tags/deezer/albums", params={"query": "Discovery"})
        assert resp.status_code == 200
        results = resp.json()
        assert len(results) == 1
        assert results[0]["id"] == "302127"
        assert results[0]["title"] == "Discovery"
        assert results[0]["artist"] == "Daft Punk"

    # 3. Детали альбома с моком
    mock_album = {
        "id": 302127,
        "title": "Discovery",
        "artist": {"name": "Daft Punk"},
        "release_date": "2001-03-07",
        "genres": {"data": [{"name": "Electronic"}]},
        "cover_xl": "https://cdn-images.dzcdn.net/cover_xl.jpg",
        "tracks": {
            "data": [
                {
                    "track_position": 1,
                    "title": "One More Time",
                    "artist": {"name": "Daft Punk"},
                    "duration": 320,
                },
                {
                    "track_position": 2,
                    "title": "Aerodynamic",
                    "artist": {"name": "Daft Punk"},
                    "duration": 212,
                },
            ]
        },
    }

    with patch.object(DeezerClient, "get_album", new=AsyncMock(return_value=mock_album)):
        resp = auth_tags_client.get("/tags/deezer/albums/302127")
        assert resp.status_code == 200
        details = resp.json()
        assert details["id"] == "302127"
        assert details["title"] == "Discovery"
        assert details["artist"] == "Daft Punk"
        assert details["year"] == 2001
        assert details["genre"] == "Electronic"
        assert len(details["tracks"]) == 2
        assert details["tracks"][0]["title"] == "One More Time"
        assert details["tracks"][1]["title"] == "Aerodynamic"


def test_musicbrainz_album_endpoints(tags_client: TestClient, auth_tags_client: TestClient):
    """Проверка эндпоинтов поиска и деталей релиза в MusicBrainz."""
    # 1. 401 Unauthorized
    resp_search_unauth = tags_client.get("/tags/musicbrainz/albums", params={"query": "Discovery"})
    assert resp_search_unauth.status_code == 401

    resp_details_unauth = tags_client.get("/tags/musicbrainz/albums/mbid-discovery-123")
    assert resp_details_unauth.status_code == 401

    # 2. Поиск релизов с моком
    from unittest.mock import AsyncMock, patch

    from app.services.musicbrainz_client import MusicBrainzClient

    mock_search = [
        {
            "id": "mbid-discovery-123",
            "title": "Discovery",
            "artist": "Daft Punk",
            "year": 2001,
            "nb_tracks": 14,
            "country": "FR",
            "status": "Official",
            "cover_url": "https://coverartarchive.org/release/mbid-discovery-123/front-500",
        }
    ]

    with patch.object(MusicBrainzClient, "search_releases", new=AsyncMock(return_value=mock_search)):
        resp = auth_tags_client.get("/tags/musicbrainz/albums", params={"query": "Discovery"})
        assert resp.status_code == 200
        results = resp.json()
        assert len(results) == 1
        assert results[0]["id"] == "mbid-discovery-123"
        assert results[0]["title"] == "Discovery"
        assert results[0]["artist"] == "Daft Punk"
        assert results[0]["country"] == "FR"

    # 3. Детали релиза с моком
    mock_release = {
        "id": "mbid-discovery-123",
        "title": "Discovery",
        "artist": "Daft Punk",
        "year": 2001,
        "genre": "Electronic\\\\French House",
        "cover_url": "https://coverartarchive.org/release/mbid-discovery-123/front-500",
        "tracks": [
            {
                "track_number": 1,
                "title": "One More Time",
                "artist": "Daft Punk",
                "duration": 320,
                "disc_number": 1,
            },
            {
                "track_number": 2,
                "title": "Aerodynamic",
                "artist": "Daft Punk",
                "duration": 212,
                "disc_number": 1,
            },
        ],
    }

    with patch.object(MusicBrainzClient, "get_release", new=AsyncMock(return_value=mock_release)):
        resp = auth_tags_client.get("/tags/musicbrainz/albums/mbid-discovery-123")
        assert resp.status_code == 200
        details = resp.json()
        assert details["id"] == "mbid-discovery-123"
        assert details["title"] == "Discovery"
        assert details["artist"] == "Daft Punk"
        assert details["year"] == 2001
        assert details["genre"] == "Electronic\\\\French House"
        assert len(details["tracks"]) == 2
        assert details["tracks"][0]["title"] == "One More Time"
        assert details["tracks"][1]["title"] == "Aerodynamic"


def test_search_covers_endpoint(tags_client: TestClient, auth_tags_client: TestClient):
    """Проверка GET /tags/covers/search: 401 без авторизации и 200 с результатами."""
    # 1. 401 Unauthorized
    resp_unauth = tags_client.get("/tags/covers/search", params={"query": "Random Access Memories"})
    assert resp_unauth.status_code == 401
    assert resp_unauth.json()["error"] == "UNAUTHORIZED"

    # 2. 200 OK с моком
    from unittest.mock import AsyncMock, patch

    from app.schemas.tag import CoverSearchResultItem

    mock_covers = [
        CoverSearchResultItem(
            source="apple",
            title="Random Access Memories",
            artist="Daft Punk",
            cover_url="https://is1-ssl.mzstatic.com/image/thumb/1400x1400bb.jpg",
            width=1400,
            height=1400,
            year=2013,
        )
    ]

    with patch.object(TagService, "search_covers", new=AsyncMock(return_value=mock_covers)):
        resp = auth_tags_client.get("/tags/covers/search", params={"query": "Random Access Memories"})
        assert resp.status_code == 200
        data = resp.json()
        assert len(data) == 1
        assert data[0]["source"] == "apple"
        assert data[0]["title"] == "Random Access Memories"
        assert data[0]["cover_url"] == "https://is1-ssl.mzstatic.com/image/thumb/1400x1400bb.jpg"
        assert data[0]["width"] == 1400


def test_batch_save_with_cover_url(
    auth_tags_client: TestClient, temp_storage: dict[str, Path]
):
    """Проверка загрузки обложки по URL через cover_url в PUT /tags/batch."""
    from unittest.mock import AsyncMock, patch

    music_dir = temp_storage["music_dir"]
    track_file = music_dir / "01. URLSong.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    payload = {
        "album": {
            "album": "URLAlbum",
            "album_artist": "URLArtist",
            "year": 2026,
            "genre": "Synthwave",
        },
        "tracks": [
            {
                "path": "01. URLSong.mp3",
                "track_number": 1,
                "title": "URLSong",
                "artist": "URLArtist",
            }
        ],
        "source_dir": "",
        "cover_url": "https://example.com/cover.jpg",
        "apply_album_to_tracks": True,
        "reorganize": True,
    }

    with patch.object(TagService, "download_cover", new=AsyncMock(return_value=(DUMMY_JPEG, "image/jpeg"))):
        resp = auth_tags_client.put("/tags/batch", json=payload)
        assert resp.status_code == 200
        data = resp.json()
        assert data["success"] is True

        target_dir = music_dir / data["destination_dir"]
        cover_file = target_dir / "cover.jpg"
        assert cover_file.exists()
        assert cover_file.read_bytes() == DUMMY_JPEG


def test_batch_save_with_disc_numbers(
    auth_tags_client: TestClient, temp_storage: dict[str, Path]
):
    """Проверка сохранения номеров дисков (disc_number, disc_total) через PUT /tags/batch."""
    music_dir = temp_storage["music_dir"]
    t1 = music_dir / "01. Disc1Track.mp3"
    t2 = music_dir / "01. Disc2Track.mp3"
    t1.write_bytes(MINIMAL_MP3)
    t2.write_bytes(MINIMAL_MP3)

    payload = {
        "album": {
            "album": "Double Album",
            "album_artist": "Epic Band",
            "disc_total": 2,
            "year": 2024,
        },
        "tracks": [
            {
                "path": "01. Disc1Track.mp3",
                "disc_number": 1,
                "disc_total": 2,
                "track_number": 1,
                "track_total": 5,
                "title": "Disc 1 Track 1",
                "artist": "Epic Band",
            },
            {
                "path": "01. Disc2Track.mp3",
                "disc_number": 2,
                "disc_total": 2,
                "track_number": 1,
                "track_total": 6,
                "title": "Disc 2 Track 1",
                "artist": "Epic Band",
            },
        ],
        "source_dir": "",
        "apply_album_to_tracks": True,
        "reorganize": False,
    }

    resp = auth_tags_client.put("/tags/batch", json=payload)
    assert resp.status_code == 200

    svc = TagService(music_sandbox=PathSandbox(music_dir))
    tags1 = svc.read_tags(t1, rel_path="01. Disc1Track.mp3")
    assert tags1.disc_number == 1
    assert tags1.disc_total == 2
    assert tags1.track_number == 1
    assert tags1.track_total == 5

    tags2 = svc.read_tags(t2, rel_path="01. Disc2Track.mp3")
    assert tags2.disc_number == 2
    assert tags2.disc_total == 2
    assert tags2.track_number == 1
    assert tags2.track_total == 6


def test_batch_save_with_per_track_covers_and_singles_mode(
    auth_tags_client: TestClient, temp_storage: dict[str, Path]
):
    """Проверка сохранения пакета с индивидуальными обложками треков (cover_base64),
    записью их в ID3v2.4 APIC и организацией в режиме синглов.
    """
    import base64

    tmp_dir = temp_storage["tmp_dir"]
    music_dir = temp_storage["music_dir"]

    job_dir = tmp_dir / "job_singles_batch" / "converted"
    job_dir.mkdir(parents=True, exist_ok=True)

    t1 = job_dir / "01. Hit One.mp3"
    t2 = job_dir / "02. Hit Two.mp3"
    t1.write_bytes(MINIMAL_MP3)
    t2.write_bytes(MINIMAL_MP3)

    cov1_b64 = "data:image/jpeg;base64," + base64.b64encode(DUMMY_JPEG).decode()
    cov2_b64 = "data:image/png;base64," + base64.b64encode(DUMMY_PNG).decode()

    payload = {
        "album": {
            "album_artist": "Singles Artist",
            "year": 2024,
        },
        "tracks": [
            {
                "path": "job_singles_batch/converted/01. Hit One.mp3",
                "track_number": 1,
                "title": "Hit One",
                "artist": "Artist Alpha",
                "album": "Single One",
                "year": 2023,
                "cover_base64": cov1_b64,
            },
            {
                "path": "job_singles_batch/converted/02. Hit Two.mp3",
                "track_number": 1,
                "title": "Hit Two",
                "artist": "Artist Beta",
                "album": "Single Two",
                "year": 2024,
                "cover_base64": cov2_b64,
            },
        ],
        "source_dir": "job_singles_batch/converted",
        "singles_mode": True,
        "group_by_album": True,
    }

    resp = auth_tags_client.put("/tags/batch", json=payload)
    assert resp.status_code == 200
    data = resp.json()
    assert data["success"] is True

    # Проверяем, что оба трека успешно перемещены и содержат свои ID3v2.4 APIC обложки
    svc = TagService(music_sandbox=PathSandbox(music_dir))

    t1_rel = data["tracks"][0]["path"]
    t2_rel = data["tracks"][1]["path"]

    t1_abs = music_dir / t1_rel
    t2_abs = music_dir / t2_rel

    assert t1_abs.exists()
    assert t2_abs.exists()

    # Проверка наличия cover.jpg / cover.png в папках синглов
    assert (t1_abs.parent / "cover.jpg").exists()
    assert (t2_abs.parent / "cover.png").exists()

    # Проверка вшитой обложки в сам MP3 через TagService
    cov1_res = svc.read_cover(t1_abs)
    assert cov1_res is not None
    cov1_art, mime1 = cov1_res
    assert cov1_art is not None
    assert mime1 == "image/jpeg"

    cov2_res = svc.read_cover(t2_abs)
    assert cov2_res is not None
    cov2_art, mime2 = cov2_res
    assert cov2_art is not None
    assert mime2 == "image/png"


def test_batch_save_with_track_cover_path(
    auth_tags_client: TestClient, temp_storage: dict[str, Path]
):
    """Проверка сохранения нескольких релизов из разных задач с путями к обложкам через track.cover_path."""
    tmp_dir = temp_storage["tmp_dir"]
    music_dir = temp_storage["music_dir"]

    # Создаем 2 разные задачи в TMP_DIR
    job1_dir = tmp_dir / "job_cov1" / "converted"
    job1_dir.mkdir(parents=True, exist_ok=True)
    t1 = job1_dir / "01. Song A.mp3"
    t1.write_bytes(MINIMAL_MP3)
    (tmp_dir / "job_cov1" / "cover.jpg").write_bytes(DUMMY_JPEG)

    job2_dir = tmp_dir / "job_cov2" / "converted"
    job2_dir.mkdir(parents=True, exist_ok=True)
    t2 = job2_dir / "01. Song B.mp3"
    t2.write_bytes(MINIMAL_MP3)
    (tmp_dir / "job_cov2" / "cover.png").write_bytes(DUMMY_PNG)

    payload = {
        "album": {
            "album_artist": "",
            "album": "",
            "year": 2024,
        },
        "tracks": [
            {
                "path": "tmp_downloads/job_cov1/converted/01. Song A.mp3",
                "track_number": 1,
                "title": "Song A",
                "artist": "Artist One",
                "album_artist": "Artist One",
                "album": "Album One",
                "year": 2023,
                "cover_path": "tmp_downloads/job_cov1/cover.jpg",
            },
            {
                "path": "tmp_downloads/job_cov2/converted/01. Song B.mp3",
                "track_number": 1,
                "title": "Song B",
                "artist": "Artist Two",
                "album_artist": "Artist Two",
                "album": "Album Two",
                "year": 2024,
                "cover_path": "tmp_downloads/job_cov2/cover.png",
            },
        ],
        "source_dir": "tmp_downloads",
        "apply_album_to_tracks": False,
        "singles_mode": False,
        "group_by_album": True,
    }

    resp = auth_tags_client.put("/tags/batch", json=payload)
    assert resp.status_code == 200
    data = resp.json()
    assert data["success"] is True

    svc = TagService(music_sandbox=PathSandbox(music_dir))

    t1_abs = music_dir / "Artist One/Album One - 2023/01. Song A.mp3"
    t2_abs = music_dir / "Artist Two/Album Two - 2024/01. Song B.mp3"

    assert t1_abs.exists()
    assert t2_abs.exists()

    # Проверяем, что обложки записались в MP3 и файлы cover.* создались
    assert (t1_abs.parent / "cover.jpg").exists()
    assert (t2_abs.parent / "cover.png").exists()

    cov1 = svc.read_cover(t1_abs)
    assert cov1 is not None
    assert cov1[1] == "image/jpeg"

    cov2 = svc.read_cover(t2_abs)
    assert cov2 is not None
    assert cov2[1] == "image/png"

    # Проверяем очистку временных директорий обеих задач
    assert not (tmp_dir / "job_cov1").exists()
    assert not (tmp_dir / "job_cov2").exists()





