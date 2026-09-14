"""Pydantic-схемы данных для подсистемы загрузки (Downloader Engine).

Определяет структуры запросов, ответов, статусов задач и нормализованных
метаданных треков, связывающих аудиопотоки с метаданными релизов.
"""

from enum import Enum
from typing import Literal

from pydantic import BaseModel, Field


class JobStatus(str, Enum):
    """Статусы жизненного цикла задачи загрузки."""

    QUEUED = "QUEUED"
    RESOLVING = "RESOLVING"
    DOWNLOADING = "DOWNLOADING"
    CONVERTING = "CONVERTING"
    TAGGING = "TAGGING"
    READY = "READY"
    ERROR = "ERROR"


class CreateDownloadRequest(BaseModel):
    """Запрос на создание новой задачи загрузки."""

    url: str = Field(
        ...,
        description="URL трека, альбома или плейлиста (YouTube, SoundCloud, Deezer)",
        examples=["https://www.deezer.com/album/302127"],
    )
    source: Literal["auto", "soundcloud", "youtube", "deezer", "yandex", "spotify", "applemusic", "torrent"] = Field(
        default="auto",
        description="Явное указание источника или 'auto' для автоматического определения по URL",
    )


class CreateBatchDownloadRequest(BaseModel):
    """Запрос на пакетное создание задач загрузки."""

    urls: list[str] = Field(
        ...,
        min_length=1,
        description="Список URL треков, альбомов или плейлистов",
        examples=[["https://www.deezer.com/album/302127", "https://soundcloud.com/artist/sets/album"]],
    )
    source: Literal["auto", "soundcloud", "youtube", "deezer", "yandex", "spotify", "applemusic", "torrent"] = Field(
        default="auto",
        description="Явное указание источника или 'auto' для автоматического определения",
    )


class ResolvedTrack(BaseModel):
    """Нормализованное представление трека, объединяющее метаданные и аудиопоток."""

    track_id: str = Field(
        ...,
        description="Уникальный идентификатор трека в рамках задачи",
    )
    track_number: int = Field(
        default=1,
        ge=1,
        description="Порядковый номер трека на диске",
    )
    title: str = Field(
        ...,
        description="Название трека",
    )
    artist: str = Field(
        ...,
        description="Исполнители трека (формат двойного слэша \\\\ для нескольких артистов)",
    )
    album: str = Field(
        default="",
        description="Название альбома",
    )
    album_artist: str = Field(
        default="",
        description="Основной исполнитель альбома",
    )
    year: int | None = Field(
        default=None,
        description="Год выпуска релиза",
    )
    genre: str | None = Field(
        default=None,
        description="Жанр",
    )
    duration: int = Field(
        default=0,
        ge=0,
        description="Длительность трека в секундах",
    )
    isrc: str | None = Field(
        default=None,
        description="Международный стандартный идентификационный номер аудиозаписи (ISRC)",
    )
    cover_url: str | None = Field(
        default=None,
        description="Прямой URL обложки максимального разрешения",
    )
    audio_source_url: str = Field(
        default="",
        description="URL аудиопотока для загрузки через yt-dlp",
    )
    converted_path: str | None = Field(
        default=None,
        description="Относительный безопасный путь к готовому MP3 внутри песочницы TMP_DIR",
    )
    status: str = Field(
        default="pending",
        description="Текущий статус трека (pending, downloading, converting, ready, error)",
    )
    error: str | None = Field(
        default=None,
        description="Текст ошибки при сбое обработки трека",
    )


class DownloadJobResponse(BaseModel):
    """Схема ответа с текущим статусом и прогрессом задачи загрузки (для polling)."""

    id: str = Field(
        ...,
        description="Уникальный идентификатор задачи (UUID4)",
    )
    status: JobStatus = Field(
        ...,
        description="Текущий статус выполнения задачи",
    )
    source: str = Field(
        ...,
        description="Тип источника (soundcloud, youtube, deezer)",
    )
    url: str = Field(
        ...,
        description="Исходный URL запроса",
    )
    progress: float = Field(
        default=0.0,
        ge=0.0,
        le=100.0,
        description="Общий процент выполнения задачи (0.0 — 100.0)",
    )
    current_step: str = Field(
        default="",
        description="Человекочитаемое описание текущего шага конвейера",
    )
    total_tracks: int = Field(
        default=0,
        ge=0,
        description="Общее количество треков в релизе",
    )
    completed_tracks: int = Field(
        default=0,
        ge=0,
        description="Количество успешно сконвертированных треков",
    )
    error: str | None = Field(
        default=None,
        description="Текст фатальной ошибки задачи, если статус ERROR",
    )
    created_at: str = Field(
        ...,
        description="ISO-таймстемп создания задачи",
    )


class BatchDownloadResponse(BaseModel):
    """Ответ на создание пакета задач загрузки."""

    job_ids: list[str] = Field(..., description="Список идентификаторов созданных задач")
    jobs: list[DownloadJobResponse] = Field(..., description="Список созданных задач со статусом QUEUED")
    total_queued: int = Field(..., description="Количество поставленных в очередь задач")


class QueueSummaryResponse(BaseModel):
    """Сводка состояния очереди загрузок для плавающего виджета и мониторинга."""

    active_jobs: int = Field(default=0, description="Количество активно скачиваемых задач")
    queued_jobs: int = Field(default=0, description="Количество задач в очереди ожидания")
    completed_jobs: int = Field(default=0, description="Количество успешно завершенных задач")
    error_jobs: int = Field(default=0, description="Количество задач с ошибкой")
    total_tracks: int = Field(default=0, description="Общее число треков во всех актуальных задачах")
    completed_tracks: int = Field(default=0, description="Число успешно скачанных/сконвертированных треков")
    overall_progress: float = Field(default=0.0, ge=0.0, le=100.0, description="Агрегированный процент выполнения очереди")


class DownloadTracksResponse(BaseModel):
    """Схема ответа со списком черновых треков для экрана проверки (Tag Editor)."""

    job_id: str = Field(
        ...,
        description="Идентификатор завершенной задачи",
    )
    album_artist: str = Field(
        default="",
        description="Исполнитель альбома",
    )
    album: str = Field(
        default="",
        description="Название альбома",
    )
    year: int | None = Field(
        default=None,
        description="Год релиза",
    )
    genre: str | None = Field(
        default=None,
        description="Жанр релиза",
    )
    cover_url: str | None = Field(
        default=None,
        description="URL оригинальной обложки",
    )
    cover_path: str | None = Field(
        default=None,
        description="Относительный безопасный путь к локально сохраненной обложке в песочнице",
    )
    tracks: list[ResolvedTrack] = Field(
        default_factory=list,
        description="Список треков с предзаполненными метаданными",
    )


class DiscographyReleaseItem(BaseModel):
    """Описание релиза (альбома, сингла, EP, сборника) в дискографии исполнителя."""

    id: str = Field(..., description="Идентификатор релиза в сервисе-источнике")
    title: str = Field(..., description="Название релиза")
    type: str = Field(default="album", description="Тип релиза (album, single, ep, compilation)")
    year: int | None = Field(default=None, description="Год выпуска")
    track_count: int | None = Field(default=None, description="Количество треков в релизе")
    cover_url: str | None = Field(default=None, description="Прямой URL обложки")
    url: str = Field(..., description="Прямой веб-URL релиза для скачивания")


class ArtistDiscographyResponse(BaseModel):
    """Сводная информация о дискографии артиста со списком релизов."""

    artist_id: str = Field(..., description="Идентификатор исполнителя")
    artist_name: str = Field(..., description="Имя артиста / название группы")
    artist_avatar: str | None = Field(default=None, description="Аватар/фото исполнителя")
    source: str = Field(..., description="Сервис-источник (deezer, spotify, yandex, applemusic, youtube)")
    total_releases: int = Field(..., description="Общее число доступных релизов")
    releases: list[DiscographyReleaseItem] = Field(default_factory=list, description="Список релизов исполнителя")
