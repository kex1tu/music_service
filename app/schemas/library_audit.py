"""Pydantic-схемы данных для подсистемы глобального поиска и аудита медиатеки.

Определяет структуры ответов поиска (артисты, альбомы, треки), сводки аудита,
списков треков без текстов/обложек, групп дубликатов и прогресса пакетного сбора текстов.
"""

from typing import Literal

from pydantic import BaseModel, Field


class SearchArtistItem(BaseModel):
    """Элемент поиска: Артист."""

    name: str = Field(..., description="Имя исполнителя")
    track_count: int = Field(default=0, description="Количество треков исполнителя в медиатеке")
    first_path: str = Field(default="", description="Относительный путь к первому найденному треку или папке")


class SearchAlbumItem(BaseModel):
    """Элемент поиска: Альбом."""

    album: str = Field(..., description="Название альбома")
    album_artist: str = Field(..., description="Основной исполнитель альбома")
    year: int | None = Field(default=None, description="Год выпуска")
    path: str = Field(..., description="Относительный путь к каталогу альбома")
    track_count: int = Field(default=0, description="Количество треков в альбоме")
    has_cover: bool = Field(default=False, description="Наличие графической обложки")


class SearchTrackItem(BaseModel):
    """Элемент поиска: Трек."""

    path: str = Field(..., description="Относительный путь к MP3-файлу")
    title: str = Field(..., description="Название трека")
    artist: str = Field(..., description="Исполнитель трека")
    album: str = Field(default="", description="Название альбома")
    track_number: int = Field(default=1, description="Номер дорожки")
    duration: int | None = Field(default=None, description="Длительность трека в секундах")
    has_cover: bool = Field(default=False, description="Наличие вшитой обложки")
    lyrics_status: Literal["none", "lrc", "txt"] = Field(
        default="none",
        description="Статус наличия файла текста песни рядом с треком",
    )


class LibrarySearchResponse(BaseModel):
    """Результат глобального поиска по медиатеке."""

    query: str = Field(..., description="Поисковый запрос")
    artists: list[SearchArtistItem] = Field(default_factory=list, description="Найденные артисты")
    albums: list[SearchAlbumItem] = Field(default_factory=list, description="Найденные альбомы")
    tracks: list[SearchTrackItem] = Field(default_factory=list, description="Найденные треки")
    total_matches: int = Field(default=0, description="Общее число совпадений")


class AuditSummaryResponse(BaseModel):
    """Сводные метрики аудита медиатеки."""

    total_tracks: int = Field(default=0, description="Общее количество MP3 треков в медиатеке")
    total_albums: int = Field(default=0, description="Общее количество папок альбомов")
    missing_lyrics_count: int = Field(default=0, description="Количество треков без внешних файлов текстов")
    missing_covers_count: int = Field(default=0, description="Количество альбомов/треков без обложек")
    duplicates_count: int = Field(default=0, description="Количество потенциальных дубликатов")


class MissingLyricsItem(BaseModel):
    """Трек, у которого отсутствует файл текста (.lrc/.txt)."""

    path: str = Field(..., description="Относительный путь к MP3 файлу")
    title: str = Field(..., description="Название трека")
    artist: str = Field(..., description="Исполнитель трека")
    album: str = Field(default="", description="Альбом")
    album_artist: str = Field(default="", description="Исполнитель альбома")
    directory: str = Field(default="", description="Относительный путь к папке трека")


class MissingCoverItem(BaseModel):
    """Релиз или трек без графической обложки."""

    path: str = Field(..., description="Относительный путь к каталогу альбома или файлу трека")
    title: str = Field(default="", description="Название альбома или трека")
    artist: str = Field(default="", description="Исполнитель")
    album: str = Field(default="", description="Альбом")
    is_album_dir: bool = Field(default=False, description="Флаг: является ли элемент папкой альбома")
    track_count: int = Field(default=1, description="Количество треков в релизе/папке")


class DuplicateTrackItem(BaseModel):
    """Информация об одном файле-кандидате в группе дубликатов."""

    path: str = Field(..., description="Относительный путь к MP3 файлу")
    filename: str = Field(..., description="Имя файла")
    size: int | None = Field(default=None, description="Размер файла в байтах")
    size_bytes: int | None = Field(default=None, description="Размер файла в байтах (алиас для UI)")
    duration: int | None = Field(default=None, description="Длительность трека в секундах")
    bitrate: int | None = Field(default=None, description="Битрейт в kbps")
    modified_at: str = Field(..., description="Дата и время изменения файла")


class DuplicateGroupItem(BaseModel):
    """Группа треков, признанных дубликатами."""

    key: str = Field(..., description="Ключ группировки (clean_artist::clean_title)")
    title: str = Field(..., description="Название песни")
    artist: str = Field(..., description="Исполнитель")
    tracks: list[DuplicateTrackItem] = Field(..., description="Список файлов-копий")


class DuplicatesResponse(BaseModel):
    """Список обнаруженных групп дубликатов."""

    groups: list[DuplicateGroupItem] = Field(default_factory=list, description="Группы дубликатов")
    total_duplicate_files: int = Field(default=0, description="Общее число избыточных файлов дубликатов")


class BatchLyricsProgressResponse(BaseModel):
    """Прогресс пакетного автоматического сбора текстов песен."""

    is_running: bool = Field(default=False, description="Флаг активности фонового процесса сбора текстов")
    total: int = Field(default=0, description="Всего треков в очереди на поиск текстов")
    current: int = Field(default=0, description="Количество обработанных треков")
    found: int = Field(default=0, description="Успешно найдено и сохранено текстов")
    failed: int = Field(default=0, description="Не найдено текстов или ошибка")
    current_track: str = Field(default="", description="Название текущего обрабатываемого трека")
