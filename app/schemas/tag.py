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
    disc_total: int | None = Field(
        default=None,
        ge=1,
        description="Всего дисков в альбоме",
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
    track_total: int | None = Field(
        default=None,
        ge=1,
        description="Всего треков на диске (ID3v2.4 TRCK)",
    )
    disc_number: int = Field(
        default=1,
        ge=1,
        description="Номер диска (ID3v2.4 TPOS)",
    )
    disc_total: int | None = Field(
        default=None,
        ge=1,
        description="Всего дисков в релизе (ID3v2.4 TPOS)",
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
    cover_url: str | None = Field(
        default=None,
        description="Внешний URL обложки для трека (для синглов или индивидуального арта)",
    )
    cover_base64: str | None = Field(
        default=None,
        description="Base64-кодированное изображение индивидуальной обложки трека",
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
    cover_url: str | None = Field(
        default=None,
        description="Внешний URL обложки из галереи для загрузки сервером",
    )
    apply_album_to_tracks: bool = Field(
        default=True,
        description="Применить общие поля альбома к трекам перед сохранением",
    )
    reorganize: bool = Field(
        default=False,
        description="Реорганизовать файлы существующего альбома по структуре медиатеки",
    )
    singles_mode: bool = Field(
        default=False,
        description="Режим синглов: каждый трек организуется в индивидуальную папку сингла",
    )
    group_by_album: bool = Field(
        default=True,
        description="Группировать треки по альбомам при реорганизации медиатеки",
    )


class BatchSaveResponse(BaseModel):
    """Ответ на успешное сохранение тегов альбома."""

    success: bool = Field(default=True, description="Флаг успешности операции")
    destination_dir: str = Field(..., description="Каталог размещения альбома после сохранения")
    tracks: list[TrackTags] = Field(default_factory=list, description="Список сохраненных треков")


class CoverSearchResultItem(BaseModel):
    """Результат поиска обложки в мультисервисной галерее."""

    source: Literal["apple", "deezer", "yandex", "spotify", "musicbrainz"] = Field(..., description="Сервис-источник")
    title: str = Field(..., description="Название альбома / релиза")
    artist: str = Field(..., description="Исполнитель альбома")
    cover_url: str = Field(..., description="Прямой URL обложки высокого разрешения")
    width: int = Field(..., description="Ширина в пикселях")
    height: int = Field(..., description="Высота в пикселях")
    year: int | None = Field(default=None, description="Год релиза")


class RecognizeAudioRequest(BaseModel):
    """Запрос на распознавание аудиофайла через Shazam."""

    path: str = Field(..., description="Относительный путь к MP3-файлу (в библиотеке или tmp_downloads)")


class RecognizedTrackResponse(BaseModel):
    """Результат акустического распознавания трека через Shazam."""

    recognized: bool = Field(..., description="Флаг успешного распознавания")
    title: str | None = Field(default=None, description="Распознанное название трека")
    artist: str | None = Field(default=None, description="Распознанный исполнитель")
    album: str | None = Field(default=None, description="Название альбома")
    year: int | None = Field(default=None, description="Год релиза")
    genre: str | None = Field(default=None, description="Жанр композиции")
    cover_url: str | None = Field(default=None, description="URL обложки высокого разрешения")


class DeezerAlbumSearchResult(BaseModel):
    """Элемент списка результатов поиска альбомов в Deezer."""

    id: str = Field(..., description="ID альбома в Deezer")
    title: str = Field(..., description="Название альбома")
    artist: str = Field(..., description="Исполнитель альбома")
    cover_url: str | None = Field(default=None, description="Обложка альбома")
    nb_tracks: int = Field(default=0, description="Количество треков")
    year: int | None = Field(default=None, description="Год выпуска")


class DeezerAlbumTrackItem(BaseModel):
    """Метаданные дорожки альбома из Deezer."""

    track_number: int = Field(..., description="Порядковый номер дорожки")
    title: str = Field(..., description="Название трека")
    artist: str = Field(..., description="Исполнитель")
    duration: int | None = Field(default=None, description="Длительность в секундах")


class DeezerAlbumDetailsResponse(BaseModel):
    """Полные метаданные альбома из Deezer для автозаполнения редактора тегов."""

    id: str = Field(..., description="ID альбома в Deezer")
    title: str = Field(..., description="Название альбома")
    artist: str = Field(..., description="Исполнитель альбома")
    year: int | None = Field(default=None, description="Год релиза")
    genre: str | None = Field(default=None, description="Основной жанр")
    cover_url: str | None = Field(default=None, description="URL обложки высокого разрешения (1000x1000)")
    tracks: list[DeezerAlbumTrackItem] = Field(default_factory=list, description="Список треков альбома")


class MusicBrainzReleaseSearchResult(BaseModel):
    """Элемент списка результатов поиска релизов в MusicBrainz."""

    id: str = Field(..., description="MBID релиза в MusicBrainz")
    title: str = Field(..., description="Название релиза / альбома")
    artist: str = Field(..., description="Исполнитель альбома")
    cover_url: str | None = Field(default=None, description="Обложка альбома из Cover Art Archive")
    nb_tracks: int = Field(default=0, description="Количество треков")
    year: int | None = Field(default=None, description="Год выпуска")
    country: str | None = Field(default=None, description="Страна релиза")
    status: str | None = Field(default=None, description="Статус релиза (Official, Bootleg и др.)")


class MusicBrainzReleaseTrackItem(BaseModel):
    """Метаданные дорожки альбома из MusicBrainz."""

    track_number: int = Field(..., description="Порядковый номер дорожки")
    title: str = Field(..., description="Название трека")
    artist: str = Field(..., description="Исполнитель")
    duration: int | None = Field(default=None, description="Длительность в секундах")
    disc_number: int = Field(default=1, description="Номер диска / медиа-носителя")


class MusicBrainzReleaseDetailsResponse(BaseModel):
    """Полные метаданные релиза из MusicBrainz для автозаполнения редактора тегов."""

    id: str = Field(..., description="MBID релиза в MusicBrainz")
    title: str = Field(..., description="Название альбома")
    artist: str = Field(..., description="Исполнитель альбома")
    year: int | None = Field(default=None, description="Год релиза")
    genre: str | None = Field(default=None, description="Жанр / теги через \\\\")
    cover_url: str | None = Field(default=None, description="URL обложки из Cover Art Archive")
    tracks: list[MusicBrainzReleaseTrackItem] = Field(default_factory=list, description="Список треков альбома")
