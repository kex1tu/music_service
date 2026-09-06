"""Тесты модуля конфигурации Settings."""

from pathlib import Path

import pytest
from pydantic import ValidationError

from app.config import Settings


def test_settings_valid(tmp_path: Path):
    """Проверка успешной загрузки настроек при передаче всех обязательных полей."""
    music_dir = tmp_path / "music"
    settings = Settings(
        MUSIC_DIR=music_dir,
        SECRET_KEY="test_secret_key_minimum_16_chars",
        ADMIN_PASSWORD_HASH="$2b$12$validbcrypthashstringtestlength1234567890",
    )
    assert settings.MUSIC_DIR == music_dir.resolve()
    assert settings.TMP_DIR == Path("tmp_downloads").resolve()
    assert settings.ADMIN_LOGIN == "admin"
    assert settings.SESSION_TTL == 604800
    assert settings.SESSION_COOKIE_SAMESITE == "strict"
    assert settings.SESSION_COOKIE_SECURE is False
    assert settings.AUDIO_BITRATE == "320k"
    assert settings.AUDIO_SAMPLE_RATE == 44100


def test_settings_missing_required_fields():
    """Проверка выброса ValidationError при отсутствии обязательных параметров."""
    with pytest.raises(ValidationError) as exc_info:
        # Без передачи обязательных полей и без .env
        Settings(
            _env_file=None,
        )

    errors = exc_info.value.errors()
    missing_fields = {e["loc"][0] for e in errors}
    assert "MUSIC_DIR" in missing_fields
    assert "SECRET_KEY" in missing_fields
    assert "ADMIN_PASSWORD_HASH" in missing_fields


def test_settings_secret_key_too_short(tmp_path: Path):
    """Проверка валидации минимальной длины SECRET_KEY."""
    with pytest.raises(ValidationError):
        Settings(
            MUSIC_DIR=tmp_path,
            SECRET_KEY="short",
            ADMIN_PASSWORD_HASH="$2b$12$validbcrypthashstringtestlength1234567890",
            _env_file=None,
        )


def test_settings_path_normalization(tmp_path: Path):
    """Проверка автоматической нормализации путей к абсолютному виду."""
    rel_path = Path("./test_music_subfolder")
    settings = Settings(
        MUSIC_DIR=rel_path,
        SECRET_KEY="test_secret_key_minimum_16_chars",
        ADMIN_PASSWORD_HASH="$2b$12$validbcrypthashstringtestlength1234567890",
        _env_file=None,
    )
    assert settings.MUSIC_DIR.is_absolute()
    assert settings.TMP_DIR.is_absolute()
