"""Интеграционные тесты для API маршрутов текстов песен (Lyrics Router)."""

from collections.abc import Generator
from unittest.mock import AsyncMock, patch

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from app.config import Settings
from app.main import create_app
from app.routers.lyrics import router as lyrics_router
from app.schemas.lyrics import LyricsSearchResult
from app.services.lyrics_service import LyricsService


@pytest.fixture
def lyrics_app(test_settings: Settings) -> FastAPI:
    """Создает приложение с зарегистрированным маршрутизатором текстов песен."""
    app = create_app(test_settings)
    app.include_router(lyrics_router)
    return app


@pytest.fixture
def unauth_lyrics_client(lyrics_app: FastAPI) -> Generator[TestClient, None, None]:
    """Предоставляет неавторизованный TestClient."""
    with TestClient(lyrics_app) as client:
        yield client


@pytest.fixture
def auth_lyrics_client(
    lyrics_app: FastAPI,
    test_settings: Settings,
    session_token: str,
) -> Generator[TestClient, None, None]:
    """Предоставляет авторизованный TestClient с валидным сессионным cookie."""
    with TestClient(lyrics_app, cookies={test_settings.SESSION_COOKIE_NAME: session_token}) as client:
        yield client


def test_unauthenticated_requests(unauth_lyrics_client: TestClient):
    """Проверка запрета доступа ко всем эндпоинтам /lyrics без сессии (HTTP 401)."""
    resp_search = unauth_lyrics_client.get("/lyrics/search", params={"artist": "Daft Punk", "title": "One More Time"})
    assert resp_search.status_code == 401

    resp_get = unauth_lyrics_client.get("/lyrics", params={"path": "track.mp3"})
    assert resp_get.status_code == 401

    resp_post = unauth_lyrics_client.post("/lyrics", json={"track_path": "track.mp3", "content": "text"})
    assert resp_post.status_code == 401

    resp_del = unauth_lyrics_client.delete("/lyrics", params={"path": "track.mp3"})
    assert resp_del.status_code == 401


def test_get_lyrics_search_endpoint(auth_lyrics_client: TestClient):
    """Проверка работы эндпоинта поиска текстов GET /lyrics/search."""
    expected_result = LyricsSearchResult(
        lyrics="[00:10.00] Synced line\n[00:20.00] Second line",
        synced=True,
        source="lrclib",
        track_name="One More Time",
        artist_name="Daft Punk",
    )

    with patch.object(LyricsService, "search", new=AsyncMock(return_value=expected_result)):
        resp = auth_lyrics_client.get(
            "/lyrics/search",
            params={"artist": "Daft Punk", "title": "One More Time", "album": "Discovery", "duration": 320},
        )
        assert resp.status_code == 200
        data = resp.json()
        assert data["source"] == "lrclib"
        assert data["synced"] is True
        assert data["track_name"] == "One More Time"
        assert data["artist_name"] == "Daft Punk"
        assert "[00:10.00]" in data["lyrics"]


def test_get_lyrics_endpoint(auth_lyrics_client: TestClient, test_settings: Settings):
    """Проверка получения существующего текста GET /lyrics."""
    mp3_file = test_settings.MUSIC_DIR / "Daft Punk" / "Discovery" / "01. One More Time.mp3"
    mp3_file.parent.mkdir(parents=True, exist_ok=True)
    mp3_file.touch()

    # 1. Когда текста еще нет -> kind="none"
    resp_none = auth_lyrics_client.get(
        "/lyrics",
        params={"path": "Daft Punk/Discovery/01. One More Time.mp3"},
    )
    assert resp_none.status_code == 200
    data_none = resp_none.json()
    assert data_none["kind"] == "none"
    assert data_none["synced"] is False
    assert data_none["lyrics"] is None

    # 2. Создаем .lrc файл -> kind="lrc"
    lrc_file = mp3_file.with_suffix(".lrc")
    lrc_file.write_text("[00:12.34] Music's got me feeling so free", encoding="utf-8")

    resp_lrc = auth_lyrics_client.get(
        "/lyrics",
        params={"path": "Daft Punk/Discovery/01. One More Time.mp3"},
    )
    assert resp_lrc.status_code == 200
    data_lrc = resp_lrc.json()
    assert data_lrc["kind"] == "lrc"
    assert data_lrc["synced"] is True
    assert "feeling so free" in data_lrc["lyrics"]


def test_post_lyrics_endpoint(auth_lyrics_client: TestClient, test_settings: Settings):
    """Проверка сохранения текста POST /lyrics."""
    rel_path = "Daft Punk/Discovery/02. Aerodynamic.mp3"
    mp3_file = test_settings.MUSIC_DIR / "Daft Punk" / "Discovery" / "02. Aerodynamic.mp3"
    mp3_file.parent.mkdir(parents=True, exist_ok=True)
    mp3_file.touch()

    # 1. Сохранение синхронизированного текста (auto -> lrc)
    lrc_body = {
        "track_path": rel_path,
        "content": "[00:30.00] Guitar solo",
        "kind": "auto",
    }
    resp1 = auth_lyrics_client.post("/lyrics", json=lrc_body)
    assert resp1.status_code == 200
    data1 = resp1.json()
    assert data1["kind"] == "lrc"
    assert data1["synced"] is True
    assert data1["lyrics"] == "[00:30.00] Guitar solo"

    saved_lrc = mp3_file.with_suffix(".lrc")
    assert saved_lrc.exists()

    # 2. Перезапись обычным текстом (auto -> txt)
    txt_body = {
        "track_path": rel_path,
        "content": "Solo without timing",
        "kind": "auto",
    }
    resp2 = auth_lyrics_client.post("/lyrics", json=txt_body)
    assert resp2.status_code == 200
    data2 = resp2.json()
    assert data2["kind"] == "txt"
    assert data2["synced"] is False
    assert data2["lyrics"] == "Solo without timing"

    saved_txt = mp3_file.with_suffix(".txt")
    assert saved_txt.exists()
    assert not saved_lrc.exists()  # Старый lrc должен быть удален


def test_delete_lyrics_endpoint(auth_lyrics_client: TestClient, test_settings: Settings):
    """Проверка удаления текста DELETE /lyrics."""
    rel_path = "Daft Punk/Discovery/03. Digital Love.mp3"
    mp3_file = test_settings.MUSIC_DIR / "Daft Punk" / "Discovery" / "03. Digital Love.mp3"
    mp3_file.parent.mkdir(parents=True, exist_ok=True)
    mp3_file.touch()

    txt_file = mp3_file.with_suffix(".txt")
    txt_file.write_text("Some text", encoding="utf-8")

    # Удаление существующего текста
    resp_del1 = auth_lyrics_client.delete("/lyrics", params={"path": rel_path})
    assert resp_del1.status_code == 200
    data1 = resp_del1.json()
    assert data1["success"] is True
    assert data1["deleted"] is True
    assert not txt_file.exists()

    # Повторный вызов -> deleted=False
    resp_del2 = auth_lyrics_client.delete("/lyrics", params={"path": rel_path})
    assert resp_del2.status_code == 200
    data2 = resp_del2.json()
    assert data2["success"] is True
    assert data2["deleted"] is False


def test_router_path_traversal_forbidden(auth_lyrics_client: TestClient):
    """Проверка возврата HTTP 403 Forbidden при попытке Path Traversal."""
    resp_get = auth_lyrics_client.get("/lyrics", params={"path": "../../etc/passwd"})
    assert resp_get.status_code == 403
    assert resp_get.json()["error"] == "PATH_TRAVERSAL_DETECTED"

    resp_post = auth_lyrics_client.post(
        "/lyrics",
        json={"track_path": "../../../root/secret.mp3", "content": "malicious"},
    )
    assert resp_post.status_code == 403
    assert resp_post.json()["error"] == "PATH_TRAVERSAL_DETECTED"

    resp_del = auth_lyrics_client.delete("/lyrics", params={"path": "..\\..\\windows\\cmd.exe"})
    assert resp_del.status_code == 403
    assert resp_del.json()["error"] == "PATH_TRAVERSAL_DETECTED"
