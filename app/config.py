"""Модуль конфигурации приложения Music Downloader & Tag Manager.

Загружает настройки из переменных окружения и файла .env с использованием pydantic-settings.
Обеспечивает валидацию типов, путей и параметров безопасности.
"""

from pathlib import Path
from typing import Literal

from pydantic import Field, field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """Типизированные настройки приложения."""

    # Хранилище
    MUSIC_DIR: Path = Field(
        ...,
        description="Абсолютный путь к постоянной медиатеке",
    )
    TMP_DIR: Path = Field(
        default=Path("tmp_downloads"),
        description="Каталог временного буфера загрузок",
    )

    # Безопасность и аутентификация
    SECRET_KEY: str = Field(
        ...,
        min_length=16,
        description="Секретный ключ для криптографической подписи session cookies",
    )
    ADMIN_LOGIN: str = Field(
        default="admin",
        min_length=1,
        description="Логин администратора",
    )
    ADMIN_PASSWORD_HASH: str = Field(
        ...,
        min_length=20,
        description="bcrypt-хэш пароля администратора",
    )
    SESSION_TTL: int = Field(
        default=604800,
        gt=0,
        description="Время жизни сессии в секундах (по умолчанию 7 суток)",
    )
    SESSION_COOKIE_NAME: str = Field(
        default="music_manager_session",
        description="Имя сессионного cookie",
    )
    SESSION_COOKIE_SECURE: bool = Field(
        default=False,
        description="Флаг Secure для cookie (True для работы по HTTPS)",
    )
    SESSION_COOKIE_SAMESITE: Literal["strict", "lax", "none"] = Field(
        default="strict",
        description="Политика SameSite для сессионных cookie",
    )

    # Внешние API
    GENIUS_API_TOKEN: str | None = Field(
        default=None,
        description="API токен Genius (при необходимости)",
    )

    # Аудио-параметры (конвертация)
    AUDIO_BITRATE: str = Field(
        default="320k",
        description="Целевой битрейт MP3 при конвертации",
    )
    AUDIO_SAMPLE_RATE: int = Field(
        default=44100,
        description="Частота дискретизации аудио (Hz)",
    )

    # Сетевые параметры сервиса
    HOST: str = Field(
        default="127.0.0.1",
        description="Хост для привязки сокета",
    )
    PORT: int = Field(
        default=8010,
        gt=0,
        le=65535,
        description="Порт сервера",
    )
    DEBUG: bool = Field(
        default=False,
        description="Режим отладки",
    )

    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    @field_validator("MUSIC_DIR", "TMP_DIR", mode="after")
    @classmethod
    def normalize_paths(cls, value: Path) -> Path:
        """Нормализует путь и преобразует его в абсолютный."""
        return value.expanduser().resolve()
