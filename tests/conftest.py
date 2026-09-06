"""Фикстуры для тестирования приложения Music Downloader & Tag Manager."""

from collections.abc import Generator
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from app.config import Settings
from app.core.sandbox import PathSandbox
from app.core.security import create_session_token
from app.main import create_app

TEST_ADMIN_PASSWORD = "testpassword123"
TEST_ADMIN_PASSWORD_HASH = "$2b$10$SamtOpUQ/ou2A/Jnx9SNJeHkTZOiekGb5aR..O1vQG8nhUYAMZPLm"


@pytest.fixture
def temp_storage(tmp_path: Path) -> dict[str, Path]:
    """Создает изолированные временные каталоги для тестов."""
    music_dir = tmp_path / "music_library"
    tmp_dir = tmp_path / "tmp_downloads"
    music_dir.mkdir(parents=True, exist_ok=True)
    tmp_dir.mkdir(parents=True, exist_ok=True)
    return {
        "music_dir": music_dir,
        "tmp_dir": tmp_dir,
    }


@pytest.fixture
def test_settings(temp_storage: dict[str, Path]) -> Settings:
    """Возвращает настройки приложения для тестового окружения."""
    return Settings(
        MUSIC_DIR=temp_storage["music_dir"],
        TMP_DIR=temp_storage["tmp_dir"],
        SECRET_KEY="test_secret_key_at_least_32_characters_long_12345",
        ADMIN_LOGIN="admin",
        ADMIN_PASSWORD_HASH=TEST_ADMIN_PASSWORD_HASH,
        SESSION_TTL=3600,
        SESSION_COOKIE_NAME="music_manager_session_test",
        SESSION_COOKIE_SECURE=False,
        SESSION_COOKIE_SAMESITE="strict",
        DEBUG=True,
    )


@pytest.fixture
def client(test_settings: Settings) -> Generator[TestClient, None, None]:
    """Предоставляет TestClient с корректным запуском lifespan."""
    app = create_app(test_settings)
    with TestClient(app) as test_client:
        yield test_client


@pytest.fixture
def session_token(test_settings: Settings) -> str:
    """Генерирует валидный сессионный токен для тестов."""
    return create_session_token({"sub": test_settings.ADMIN_LOGIN}, test_settings.SECRET_KEY)


@pytest.fixture
def authenticated_client(
    test_settings: Settings, session_token: str
) -> Generator[TestClient, None, None]:
    """Предоставляет TestClient с предустановленным валидным сессионным cookie."""
    app = create_app(test_settings)
    with TestClient(app, cookies={test_settings.SESSION_COOKIE_NAME: session_token}) as test_client:
        yield test_client


@pytest.fixture
def music_sandbox(test_settings: Settings) -> PathSandbox:
    """Предоставляет экземпляр PathSandbox для тестовой директории MUSIC_DIR."""
    return PathSandbox(test_settings.MUSIC_DIR)


@pytest.fixture
def tmp_sandbox(test_settings: Settings) -> PathSandbox:
    """Предоставляет экземпляр PathSandbox для тестовой директории TMP_DIR."""
    return PathSandbox(test_settings.TMP_DIR)

