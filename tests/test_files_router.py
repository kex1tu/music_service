"""Интеграционные тесты для маршрутов файлового менеджера (GET /files, DELETE /files)."""

from collections.abc import Generator
from pathlib import Path

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from app.config import Settings
from app.core.sandbox import PathSandbox
from app.main import create_app
from app.schemas.tag import TrackTags
from app.services.tag_service import TagService

MINIMAL_MP3 = b"\xff\xfb\x90\x00" + b"\x00" * 300


@pytest.fixture
def files_app(test_settings: Settings) -> FastAPI:
    """Создает приложение FastAPI с зарегистрированными маршрутами."""
    return create_app(test_settings)


@pytest.fixture
def files_client(files_app: FastAPI) -> Generator[TestClient, None, None]:
    """Неавторизованный TestClient."""
    with TestClient(files_app) as tc:
        yield tc


@pytest.fixture
def auth_files_client(
    files_app: FastAPI, test_settings: Settings, session_token: str
) -> Generator[TestClient, None, None]:
    """Авторизованный TestClient с валидной сессией."""
    with TestClient(files_app, cookies={test_settings.SESSION_COOKIE_NAME: session_token}) as tc:
        yield tc


def test_files_unauthorized(files_client: TestClient):
    """Проверка запрета доступа без аутентификации (HTTP 401)."""
    resp_get = files_client.get("/files")
    assert resp_get.status_code == 401
    assert resp_get.json()["error"] == "UNAUTHORIZED"

    resp_tracks = files_client.get("/files/tracks", params={"path": "album"})
    assert resp_tracks.status_code == 401
    assert resp_tracks.json()["error"] == "UNAUTHORIZED"

    resp_delete = files_client.delete("/files", params={"path": "track.mp3"})
    assert resp_delete.status_code == 401
    assert resp_delete.json()["error"] == "UNAUTHORIZED"


def test_list_directory_success(auth_files_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка успешного листинга каталога с файлами и директориями."""
    music_dir = temp_storage["music_dir"]

    # Создаем папку и файл
    artist_dir = music_dir / "Justice"
    artist_dir.mkdir(parents=True, exist_ok=True)

    track_file = artist_dir / "01. Genesis.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    tag_svc = TagService(music_sandbox=PathSandbox(music_dir))
    tag_svc.write_tags(
        track_file,
        TrackTags(
            path="Justice/01. Genesis.mp3",
            track_number=1,
            title="Genesis",
            artist="Justice",
            album="Cross",
            year=2007,
        ),
    )

    # 1. Листинг корня
    resp_root = auth_files_client.get("/files")
    assert resp_root.status_code == 200
    root_data = resp_root.json()
    assert root_data["current_path"] == ""
    assert root_data["parent_path"] is None
    assert root_data["total_directories"] == 1
    assert root_data["items"][0]["name"] == "Justice"
    assert root_data["items"][0]["type"] == "directory"

    # 2. Листинг поддиректории
    resp_sub = auth_files_client.get("/files", params={"path": "Justice"})
    assert resp_sub.status_code == 200
    sub_data = resp_sub.json()
    assert sub_data["current_path"] == "Justice"
    assert sub_data["parent_path"] == ""
    assert sub_data["total_files"] == 1
    file_item = sub_data["items"][0]
    assert file_item["name"] == "01. Genesis.mp3"
    assert file_item["title"] == "Genesis"
    assert file_item["artist"] == "Justice"
    assert file_item["track_number"] == 1


def test_get_album_tracks_endpoint(auth_files_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка GET /files/tracks для получения треков альбома."""
    music_dir = temp_storage["music_dir"]
    album_dir = music_dir / "M83" / "Hurry Up"
    album_dir.mkdir(parents=True, exist_ok=True)

    t1 = album_dir / "01. Intro.mp3"
    t1.write_bytes(MINIMAL_MP3)

    tag_svc = TagService(music_sandbox=PathSandbox(music_dir))
    tag_svc.write_tags(t1, TrackTags(path="M83/Hurry Up/01. Intro.mp3", track_number=1, title="Intro"))

    resp = auth_files_client.get("/files/tracks", params={"path": "M83/Hurry Up"})
    assert resp.status_code == 200
    data = resp.json()
    assert len(data) == 1
    assert data[0]["title"] == "Intro"
    assert data[0]["track_number"] == 1


def test_delete_file_endpoint(auth_files_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка DELETE /files для удаления файла."""
    music_dir = temp_storage["music_dir"]
    track_file = music_dir / "to_delete.mp3"
    track_file.write_bytes(MINIMAL_MP3)

    resp = auth_files_client.delete("/files", params={"path": "to_delete.mp3"})
    assert resp.status_code == 200
    data = resp.json()
    assert data["success"] is True
    assert data["deleted_path"] == "to_delete.mp3"
    assert data["is_directory"] is False
    assert not track_file.exists()


def test_delete_root_forbidden(auth_files_client: TestClient):
    """Инвариант: попытка удаления корня возвращает HTTP 403."""
    resp = auth_files_client.delete("/files", params={"path": ""})
    assert resp.status_code == 403
    assert resp.json()["error"] == "PATH_TRAVERSAL_DETECTED"


def test_files_path_traversal(auth_files_client: TestClient):
    """Инвариант: попытка выхода за песочницу возвращает HTTP 403."""
    resp = auth_files_client.get("/files", params={"path": "../../etc"})
    assert resp.status_code == 403
    assert resp.json()["error"] == "PATH_TRAVERSAL_DETECTED"


def test_files_not_found(auth_files_client: TestClient):
    """Проверка ошибки 404 при запросе несуществующего пути."""
    resp = auth_files_client.get("/files", params={"path": "non_existent_folder"})
    assert resp.status_code == 404
    assert resp.json()["error"] == "RESOURCE_NOT_FOUND"
