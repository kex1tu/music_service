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


def test_auto_retag_genres_endpoint(auth_files_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка POST /files/audit/retag-genres для автоматической перезаписи тегов."""
    music_dir = temp_storage["music_dir"]
    album_dir = music_dir / "Поп Альбом"
    album_dir.mkdir(parents=True, exist_ok=True)

    t1 = album_dir / "01. Трек.mp3"
    t1.write_bytes(MINIMAL_MP3)

    tag_svc = TagService(music_sandbox=PathSandbox(music_dir))
    tag_svc.write_tags(
        t1,
        TrackTags(
            path="Поп Альбом/01. Трек.mp3",
            title="Трек",
            artist="Артист",
            album="Поп Альбом",
            genre=r"Поп\\Рок",
        ),
    )

    # 1. Проверяем обнаружение через GET /files/audit/non-en-genres
    resp_list = auth_files_client.get("/files/audit/non-en-genres")
    assert resp_list.status_code == 200
    items = resp_list.json()
    assert len(items) == 1
    assert items[0]["current_genre"] == r"Поп\\Рок"
    assert items[0]["canonical_genre"] == "Rock"

    # 2. Выполняем автоперезапись через POST /files/audit/retag-genres
    resp_retag = auth_files_client.post("/files/audit/retag-genres")
    assert resp_retag.status_code == 200
    retag_data = resp_retag.json()
    assert retag_data["success"] is True
    assert retag_data["updated_count"] == 1

    # 3. Проверяем теги на диске
    updated_tags = tag_svc.read_tags(t1)
    assert updated_tags.genre == "Rock"

    # 4. Проверяем, что после перезаписи в GET /files/audit/non-en-genres список пуст
    resp_list_after = auth_files_client.get("/files/audit/non-en-genres")
    assert resp_list_after.status_code == 200
    assert len(resp_list_after.json()) == 0


def test_fix_path_endpoint(auth_files_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка POST /files/audit/fix-path для исправления пути отдельного файла."""
    music_dir = temp_storage["music_dir"]
    album_dir = music_dir / "Damn Garden"
    album_dir.mkdir(parents=True, exist_ok=True)

    # Старый существующий файл
    existing_file = album_dir / "01. Свет.mp3"
    existing_file.write_bytes(MINIMAL_MP3)

    # Некорректный файл с суффиксом (2)
    invalid_file = album_dir / "01. Свет (2).mp3"
    invalid_file.write_bytes(MINIMAL_MP3)
    invalid_lrc = album_dir / "01. Свет (2).lrc"
    invalid_lrc.write_text("[00:01.00]Текст песни", encoding="utf-8")

    tag_svc = TagService(music_sandbox=PathSandbox(music_dir))
    tag_svc.write_tags(
        invalid_file,
        TrackTags(
            path="Damn Garden/01. Свет (2).mp3",
            title="Свет",
            artist="Damn Garden",
            album="Damn Garden",
            track_number=1,
        ),
    )

    resp = auth_files_client.post(
        "/files/audit/fix-path",
        json={"path": "Damn Garden/01. Свет (2).mp3", "overwrite": True},
    )
    assert resp.status_code == 200
    data = resp.json()
    assert data["success"] is True
    assert data["source_path"] == "Damn Garden/01. Свет (2).mp3"
    assert data["target_path"] == "Damn Garden/01. Свет.mp3"

    assert existing_file.exists()
    assert not invalid_file.exists()
    assert (album_dir / "01. Свет.lrc").exists()
    assert not invalid_lrc.exists()


def test_fix_all_paths_endpoint(auth_files_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка POST /files/audit/fix-all-paths для пакетного исправления всех путей."""
    music_dir = temp_storage["music_dir"]
    bad_dir = music_dir / "OtherFolder"
    bad_dir.mkdir(parents=True, exist_ok=True)

    bad_track = bad_dir / "bad_name.mp3"
    bad_track.write_bytes(MINIMAL_MP3)

    tag_svc = TagService(music_sandbox=PathSandbox(music_dir))
    tag_svc.write_tags(
        bad_track,
        TrackTags(
            path="OtherFolder/bad_name.mp3",
            title="Proper Title",
            artist="Artist",
            album="Clean Album",
            track_number=2,
        ),
    )

    resp = auth_files_client.post("/files/audit/fix-all-paths", json={"overwrite": True})
    assert resp.status_code == 200
    data = resp.json()
    assert data["success"] is True
    assert data["fixed_count"] >= 1

    expected_path = music_dir / "Clean Album" / "02. Proper Title.mp3"
    assert expected_path.exists()
    assert not bad_track.exists()


def test_download_missing_tracks_endpoint(auth_files_client: TestClient, temp_storage: dict[str, Path]):
    """Проверка POST /files/audit/incomplete-albums/download-missing."""
    music_dir = temp_storage["music_dir"]
    album_dir = music_dir / "Test Album"
    album_dir.mkdir(parents=True, exist_ok=True)

    t1 = album_dir / "01. Track.mp3"
    t1.write_bytes(MINIMAL_MP3)

    tag_svc = TagService(music_sandbox=PathSandbox(music_dir))
    tag_svc.write_tags(
        t1,
        TrackTags(
            path="Test Album/01. Track.mp3",
            title="Track 1",
            artist="Artist",
            album="Test Album",
            track_number=1,
            track_total=2,
        ),
    )

    resp = auth_files_client.post(
        "/files/audit/incomplete-albums/download-missing",
        json={
            "path": "Test Album",
            "track_urls": ["https://www.deezer.com/track/123456"],
        },
    )
    assert resp.status_code == 200
    jobs = resp.json()
    assert len(jobs) == 1
    assert jobs[0]["url"] == "https://www.deezer.com/track/123456"

