"""Модуль конфигурации приложения Music Downloader & Tag Manager.

Загружает настройки из переменных окружения и файла .env с использованием pydantic-settings.
Обеспечивает валидацию типов, путей и параметров безопасности.
"""

from pathlib import Path
from typing import Literal

from pydantic import Field, field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict

PROJECT_ROOT = Path(__file__).resolve().parent.parent


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

    # Файлы аутентификации yt-dlp
    YTDLP_COOKIES_PATH: Path | None = Field(
        default=None,
        description="Путь к файлу cookies.txt для yt-dlp (обход возрастных ограничений)",
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
    YANDEX_MUSIC_TOKEN: str | None = Field(
        default=None,
        description="OAuth токен Яндекс Музыки (для прямого скачивания 320 kbps MP3 без YouTube)",
    )
    SPOTIFY_CLIENT_ID: str | None = Field(
        default=None,
        description="Client ID приложения Spotify (опционально для официального Web API)",
    )
    SPOTIFY_CLIENT_SECRET: str | None = Field(
        default=None,
        description="Client Secret приложения Spotify (опционально для официального Web API)",
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

    # Параметры загрузчика и очереди
    MAX_CONCURRENT_DOWNLOADS: int = Field(
        default=2,
        ge=1,
        le=10,
        description="Максимальное количество параллельных фоновых загрузок",
    )
    MATCHER_CONCURRENCY: int = Field(
        default=4,
        ge=1,
        le=10,
        description="Максимальное количество параллельных запросов сопоставления AudioMatcher (3-5)",
    )
    JOB_TTL_HOURS: int = Field(
        default=24,
        ge=1,
        le=168,
        description="Время жизни временных файлов задачи (часов) до очистки сборщиком мусора",
    )
    COBALT_API_URL: str | None = Field(
        default=None,
        description="URL шлюза Cobalt API для запасного скачивания потоков",
    )
    ALLOWED_DOWNLOAD_DOMAINS: tuple[str, ...] = (
        "youtube.com",
        "music.youtube.com",
        "youtu.be",
        "soundcloud.com",
        "on.soundcloud.com",
        "deezer.com",
        "deezer.page.link",
        "link.deezer.com",
        "music.yandex.ru",
        "music.yandex.com",
        "music.yandex.by",
        "music.yandex.kz",
        "open.spotify.com",
        "spotify.link",
        "music.apple.com",
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

    @property
    def cookies_file(self) -> Path | None:
        """Возвращает валидный путь к файлу cookies.txt, если он существует.

        Двухуровневый поиск:
        1. Если задан YTDLP_COOKIES_PATH, проверяет его относительно PROJECT_ROOT,
           CWD или как абсолютный путь.
        2. Если не задан или файл не найден, проверяет cookies.txt в PROJECT_ROOT или CWD.
        """
        if self.YTDLP_COOKIES_PATH:
            candidate = self.YTDLP_COOKIES_PATH.expanduser()
            if not candidate.is_absolute():
                resolved_root = (PROJECT_ROOT / candidate).resolve()
                if resolved_root.is_file():
                    return resolved_root
                resolved_cwd = (Path.cwd() / candidate).resolve()
                if resolved_cwd.is_file():
                    return resolved_cwd
            elif candidate.is_file():
                return candidate

        # Автоматическое обнаружение cookies.txt в корне проекта или CWD
        root_cookies = (PROJECT_ROOT / "cookies.txt").resolve()
        if root_cookies.is_file():
            return root_cookies

        cwd_cookies = (Path.cwd() / "cookies.txt").resolve()
        if cwd_cookies.is_file():
            return cwd_cookies

        return None
