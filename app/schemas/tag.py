"""Pydantic-схемы данных для подсистемы метаданных и тегов (Metadata & Tag Service).

Определяет структуры метаданных треков и альбомов, запросов пакетного сохранения
и ответов API для редактора тегов.
"""

from typing import Literal

from pydantic import BaseModel, Field


class AlbumTags(BaseModel):
    """Метаданные уровня альбома / релиза."""

    album_artist: str = Field(
        default="",
        description="Исполнитель альбома (\\\\ для нескольких исполнителей)",
    )
    album: str = Field(
        default="",
        description="Название альбома",
    )
    year: int | None = Field(
        default=None,
        ge=1000,
        le=2999,
        description="Год выпуска релиза",
    )
    genre: str | None = Field(
        default=None,
        description="Жанры релиза (\\\\ для нескольких жанров)",
    )
    cover_url: str | None = Field(
        default=None,
        description="Внешний URL обложки альбома",
    )
    cover_path: str | None = Field(
        default=None,
        description="Относительный путь к файлу обложки",
    )


class TrackTags(BaseModel):
    """Метаданные отдельного аудиотрека."""

    path: str = Field(
        ...,
        description="Относительный путь к MP3-файлу в песочнице",
    )
    track_number: int = Field(
        default=1,
        ge=1,
        description="Порядковый номер дорожки на альбоме",
    )
    title: str = Field(
        default="",
        description="Название трека",
    )
    artist: str = Field(
        default="",
        description="Исполнители трека (\\\\ для нескольких исполнителей)",
    )
    album: str = Field(
        default="",
        description="Название альбома",
    )
    album_artist: str = Field(
        default="",
        description="Исполнитель альбома (\\\\ для нескольких исполнителей)",
    )
    year: int | None = Field(
        default=None,
        ge=1000,
        le=2999,
        description="Год выпуска",
    )
    genre: str | None = Field(
        default=None,
        description="Жанры трека (\\\\ для нескольких жанров)",
    )
    duration: int | None = Field(
        default=None,
        ge=0,
        description="Длительность звучания в секундах",
    )
    lyrics_status: Literal["none", "lrc", "txt"] = Field(
        default="none",
        description="Статус наличия файла текста песни",
    )
    has_cover: bool = Field(
        default=False,
        description="Признак наличия встроенной фронтальной обложки (APIC type=3)",
    )


class AlbumDetailsResponse(BaseModel):
    """Полные сведения об альбоме и входящих в него треках."""

    album: AlbumTags = Field(..., description="Метаданные уровня альбома")
    tracks: list[TrackTags] = Field(default_factory=list, description="Список треков альбома")
    has_cover: bool = Field(default=False, description="Наличие обложки в каталоге или треках")
    cover_path: str | None = Field(default=None, description="Относительный путь к обложке")


class SaveAlbumRequest(BaseModel):
    """Запрос на пакетное сохранение тегов альбома и треков."""

    album: AlbumTags = Field(..., description="Метаданные альбома")
    tracks: list[TrackTags] = Field(..., description="Список треков с обновленными тегами")
    source_dir: str = Field(
        ...,
        description="Исходный каталог (путь в TMP_DIR или MUSIC_DIR)",
    )
    cover_path: str | None = Field(
        default=None,
        description="Относительный путь к новой обложке для внедрения",
    )
    cover_base64: str | None = Field(
        default=None,
        description="Base64-кодированное изображение обложки (data URL или сырой base64) для загрузки из браузера",
    )
    apply_album_to_tracks: bool = Field(
        default=True,
        description="Применить общие поля альбома к трекам перед сохранением",
    )
    reorganize: bool = Field(
        default=False,
        description="Реорганизовать файлы существующего альбома по структуре медиатеки",
    )


class BatchSaveResponse(BaseModel):
    """Ответ на успешное сохранение тегов альбома."""

    success: bool = Field(default=True, description="Флаг успешности операции")
    destination_dir: str = Field(..., description="Каталог размещения альбома после сохранения")
    tracks: list[TrackTags] = Field(default_factory=list, description="Список сохраненных треков")
