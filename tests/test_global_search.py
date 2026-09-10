"""Тесты глобального поиска по медиатеке (GET /files/search)."""

from pathlib import Path
from unittest.mock import AsyncMock

import pytest
from fastapi.testclient import TestClient

from app.dependencies import get_library_audit_service
from app.schemas.tag import TrackTags
from app.services.library_audit_service import LibraryAuditService


@pytest.fixture
def mock_audit_service(tmp_path: Path):
    """Создает мок сервиса аудита с наполненным тестовым кешем."""
    from app.core.sandbox import PathSandbox
    from app.services.tag_service import TagService

    sandbox = PathSandbox(tmp_path)
    tag_svc = TagService(music_sandbox=sandbox)
    audit_svc = LibraryAuditService(music_sandbox=sandbox, tag_service=tag_svc)

    # Наполняем тестовыми треками
    audit_svc._cache = {
        "Daft Punk/Discovery/01. One More Time.mp3": {
            "mtime": 1000.0,
            "size": 5000000,
            "track": TrackTags(
                path="Daft Punk/Discovery/01. One More Time.mp3",
                track_number=1,
                title="One More Time",
                artist="Daft Punk\\\\Romanthony",
                album="Discovery",
                album_artist="Daft Punk",
                year=2001,
                duration=320,
                has_cover=True,
                lyrics_status="lrc",
            ),
        },
        "Daft Punk/Discovery/02. Aerodynamic.mp3": {
            "mtime": 1000.0,
            "size": 4000000,
            "track": TrackTags(
                path="Daft Punk/Discovery/02. Aerodynamic.mp3",
                track_number=2,
                title="Aerodynamic",
                artist="Daft Punk",
                album="Discovery",
                album_artist="Daft Punk",
                year=2001,
                duration=210,
                has_cover=True,
                lyrics_status="none",
            ),
        },
        "Radiohead/OK Computer/01. Airbag.mp3": {
            "mtime": 1000.0,
            "size": 6000000,
            "track": TrackTags(
                path="Radiohead/OK Computer/01. Airbag.mp3",
                track_number=1,
                title="Airbag",
                artist="Radiohead",
                album="OK Computer",
                album_artist="Radiohead",
                year=1997,
                duration=284,
                has_cover=False,
                lyrics_status="txt",
            ),
        },
    }
    audit_svc.ensure_index = AsyncMock()
    return audit_svc


def test_search_unauthorized(client: TestClient):
    """Проверка запрета доступа к поиску без авторизации."""
    resp = client.get("/files/search?query=daft")
    assert resp.status_code == 401


def test_search_by_artist(authenticated_client: TestClient, mock_audit_service):
    """Поиск по исполнителю."""
    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: mock_audit_service
    try:
        resp = authenticated_client.get("/files/search?query=daft")
        assert resp.status_code == 200
        data = resp.json()
        assert data["query"] == "daft"
        assert len(data["artists"]) >= 1
        assert any(a["name"] == "Daft Punk" for a in data["artists"])
        assert len(data["tracks"]) >= 2
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)


def test_search_by_album(authenticated_client: TestClient, mock_audit_service):
    """Поиск по названию альбома."""
    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: mock_audit_service
    try:
        resp = authenticated_client.get("/files/search?query=Computer")
        assert resp.status_code == 200
        data = resp.json()
        assert len(data["albums"]) == 1
        assert data["albums"][0]["album"] == "OK Computer"
        assert len(data["tracks"]) == 1
        assert data["tracks"][0]["title"] == "Airbag"
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)


def test_search_by_track_title(authenticated_client: TestClient, mock_audit_service):
    """Поиск по названию конкретной песни."""
    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: mock_audit_service
    try:
        resp = authenticated_client.get("/files/search?query=Aerodynamic")
        assert resp.status_code == 200
        data = resp.json()
        assert len(data["tracks"]) == 1
        assert data["tracks"][0]["title"] == "Aerodynamic"
        assert data["tracks"][0]["lyrics_status"] == "none"
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)


def test_search_case_insensitive(authenticated_client: TestClient, mock_audit_service):
    """Проверка нечувствительности к регистру букв."""
    authenticated_client.app.dependency_overrides[get_library_audit_service] = lambda: mock_audit_service
    try:
        resp1 = authenticated_client.get("/files/search?query=RADIOHEAD")
        resp2 = authenticated_client.get("/files/search?query=radiohead")
        assert resp1.status_code == 200
        assert resp2.status_code == 200
        assert resp1.json()["total_matches"] == resp2.json()["total_matches"]
    finally:
        authenticated_client.app.dependency_overrides.pop(get_library_audit_service, None)
