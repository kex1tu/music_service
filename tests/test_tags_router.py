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


