"""Тесты инициализации приложения, жизненного цикла, middleware и обработки исключений."""

from pathlib import Path
from unittest.mock import patch

import pytest
from fastapi import Request
from fastapi.testclient import TestClient
from pydantic import BaseModel

from app.config import Settings
from app.core.exceptions import (
    PathTraversalError,
    StorageConfigurationError,
    TagWriteError,
)
from app.main import create_app, verify_storage_directory


def test_health_endpoint(client: TestClient):
    """Проверка доступности эндпоинта /api/health."""
    response = client.get("/api/health")
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "ok"
    assert data["app"] == "music-manager"
    assert "version" in data


def test_index_page(client: TestClient):
    """Проверка рендеринга главной страницы index.html."""
    response = client.get("/")
    assert response.status_code == 200
    assert "text/html" in response.headers["content-type"]
    assert "Music Downloader & Tag Manager" in response.text


def test_verify_storage_directory_success(tmp_path: Path):
    """Проверка успешной верификации доступного для записи каталога."""
    target_dir = tmp_path / "valid_dir"
    verify_storage_directory(target_dir, "VALID_DIR")
    assert target_dir.exists()
    assert target_dir.is_dir()


def test_verify_storage_directory_failure(tmp_path: Path):
    """Проверка выброса StorageConfigurationError при сбое записи в каталог."""
    target_dir = tmp_path / "failing_dir"
    target_dir.mkdir(parents=True, exist_ok=True)

    with patch.object(Path, "write_text", side_effect=PermissionError("Read-only file system")):
        with pytest.raises(StorageConfigurationError) as exc_info:
            verify_storage_directory(target_dir, "FAILING_DIR")
        assert "недоступен для записи" in str(exc_info.value)


def test_lifespan_fail_fast_on_storage_error(tmp_path: Path):
    """Проверка сбоя старта приложения при недоступности хранилища."""
    bad_settings = Settings(
        MUSIC_DIR=tmp_path / "unwritable_music",
        TMP_DIR=tmp_path / "unwritable_tmp",
        SECRET_KEY="test_secret_key_minimum_16_chars",
        ADMIN_PASSWORD_HASH="$2b$12$validbcrypthashstringtestlength1234567890",
        _env_file=None,
    )

    app = create_app(bad_settings)

    with (
        patch("app.main.verify_storage_directory", side_effect=StorageConfigurationError("Disk failure")),
        pytest.raises(StorageConfigurationError),
        TestClient(app),
    ):
        pass


def test_domain_exception_handling(test_settings: Settings):
    """Проверка перехвата и корректной трансляции доменных исключений в HTTP-статусы."""
    app = create_app(test_settings)

    @app.get("/test-tag-error")
    async def trigger_tag_error():
        raise TagWriteError(
            "Не удалось записать теги ID3v2.4",
            details={"file": "sample.mp3", "frame": "TIT2"},
        )

    @app.get("/test-traversal-error")
    async def trigger_traversal_error():
        raise PathTraversalError("Попытка доступа к запрещенному пути")

    with TestClient(app) as test_client:
        # 1. TagWriteError -> 422
        resp = test_client.get("/test-tag-error")
        assert resp.status_code == 422
        data = resp.json()
        assert data["error"] == "TAG_WRITE_FAILED"
        assert "sample.mp3" in str(data.get("details", {}))

        # 2. PathTraversalError -> 403
        resp = test_client.get("/test-traversal-error")
        assert resp.status_code == 403
        data = resp.json()
        assert data["error"] == "PATH_TRAVERSAL_DETECTED"


def test_unhandled_exception_does_not_leak_internals(test_settings: Settings):
    """Проверка сокрытия внутренней информации и трейса при ошибке 500."""
    app = create_app(test_settings)

    @app.get("/test-internal-error")
    async def trigger_internal_error():
        # Симулируем неожиданную системную ошибку
        return 1 / 0

    with TestClient(app, raise_server_exceptions=False) as test_client:
        resp = test_client.get("/test-internal-error")
        assert resp.status_code == 500
        data = resp.json()
        assert data["error"] == "INTERNAL_SERVER_ERROR"
        assert data["message"] == "Внутренняя ошибка сервера"
        # Убеждаемся, что в ответе нет упоминаний ZeroDivisionError или путей
        assert "ZeroDivisionError" not in resp.text
        assert "traceback" not in resp.text


def test_validation_error_format(test_settings: Settings):
    """Проверка структурированного ответа при ошибке валидации тела запроса (422)."""
    app = create_app(test_settings)

    class Payload(BaseModel):
        count: int

    @app.post("/test-validation")
    async def receive_payload(body: Payload):
        return {"count": body.count}

    with TestClient(app) as test_client:
        resp = test_client.post("/test-validation", json={"count": "not-an-integer"})
        assert resp.status_code == 422
        data = resp.json()
        assert data["error"] == "VALIDATION_ERROR"
        assert "details" in data


def test_session_middleware_cookie_attributes(test_settings: Settings):
    """Проверка установки сессионного cookie со строгими атрибутами безопасности."""
    app = create_app(test_settings)

    @app.post("/test-set-session")
    async def set_session(request: Request):
        request.session["user"] = "admin"
        return {"status": "session_saved"}

    @app.get("/test-get-session")
    async def get_session(request: Request):
        return {"user": request.session.get("user")}

    with TestClient(app) as test_client:
        resp = test_client.post("/test-set-session")
        assert resp.status_code == 200

        # Проверяем заголовок set-cookie
        set_cookie_header = resp.headers.get("set-cookie", "").lower()
        assert test_settings.SESSION_COOKIE_NAME.lower() in set_cookie_header
        assert "httponly" in set_cookie_header
        assert "samesite=strict" in set_cookie_header

        # Проверяем сохранение состояния сессии в следующем запросе
        resp_get = test_client.get("/test-get-session")
        assert resp_get.status_code == 200
        assert resp_get.json()["user"] == "admin"
