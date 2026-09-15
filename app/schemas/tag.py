"""Pydantic-схемы данных для подсистемы метаданных и тегов (Metadata & Tag Service).

Определяет структуры метаданных треков и альбомов, запросов пакетного сохранения
и ответов API для редактора тегов.
"""

import re
from typing import Any, Literal

from pydantic import BaseModel, Field, field_validator

CYRILLIC_GENRE_REGEX = re.compile(r"[\u0400-\u04FF]")

RUSSIAN_GENRE_MAP: dict[str, str] = {
    "поп\\рок": "Pop \\\\Rock\\\\Pop-Rock",
    "поп/рок": "Pop \\\\Rock\\\\Pop-Rock",
    "поп-рок": "Pop \\\\Rock\\\\Pop-Rock",
    "поп рок": "Pop \\\\Rock\\\\Pop-Rock",
    "альтернативная музыка": "Alternative",
    "альтернатива": "Alternative",
    "танцевальная музыка": "Dance Music",
    "танцевальная": "Dance Music",
    "русский рэп": "Russian Hip-Hop",
    "рэп": "Hip-Hop",
    "хип-хоп": "Hip-Hop",
    "хип хоп": "Hip-Hop",
    "хипхоп": "Hip-Hop",
    "русский хип-хоп": "Russian Hip-Hop",
    "рок": "Rock",
    "русский рок": "Russian Rock",
    "альтернативный рок": "Alternative Rock",
    "поп": "Pop",
    "поп-музыка": "Pop",
    "поп музыка": "Pop",
    "русский поп": "Russian Pop",
    "электроника": "Electronic",
    "электронная музыка": "Electronic",
    "шансон": "Chanson",
    "русский шансон": "Russian Chanson",
    "метал": "Metal",
    "металл": "Metal",
    "хэви-метал": "Heavy Metal",
    "тяжелый рок": "Hard Rock",
    "тяжёлый рок": "Hard Rock",
    "джаз": "Jazz",
    "блюз": "Blues",
    "фонк": "Phonk",
    "дрилл": "Drill",
    "панк": "Punk",
    "панк-рок": "Punk Rock",
    "панк рок": "Punk Rock",
    "инди": "Indie",
    "инди-рок": "Indie Rock",
    "диско": "Disco",
    "регги": "Reggae",
    "классика": "Classical",
    "классическая музыка": "Classical",
    "саундтрек": "Soundtrack",
    "эмбиент": "Ambient",
    "хаус": "House",
    "техно": "Techno",
    "транс": "Trance",
    "драм-н-бэйс": "Drum and Bass",
    "дабстеп": "Dubstep",
}


def normalize_genre_item(item: str) -> str:
    """Нормализует отдельный жанр, приводя 'Hip-Hop' к каноническому виду через дефис."""
    cleaned = item.strip()
    if not cleaned:
        return ""
    # Hip-Hop строго пишется как 'Hip-Hop' (через дефис)
    return re.sub(r"(?i)\b(?:hip[- ]?hop|хип[- ]?хоп)\b", "Hip-Hop", cleaned)


ROCK_REGEX = re.compile(
    r"(?i)(?:^|[\s/\\;,\-|_\(\)\[\]])(rock|рок)(?:$|[\s/\\;,\-|_\(\)\[\]])|rock|рок"
)
HIP_POP_RAP_REGEX = re.compile(
    r"(?i)\b(?:pop|поп|hip[- ]?hop|хип[- ]?хоп|хипхоп|hip|хип|rap|рэп)\b|"
    r"(?:^|[\s/\\;,\-|_\(\)\[\]])(?:pop|поп|hip|хип|rap|рэп)(?:$|[\s/\\;,\-|_\(\)\[\]])|"
    r"hip[- ]?hop|хип[- ]?хоп|хипхоп|pop|поп|рэп|\brap\b"
)


def canonicalize_genre(raw: Any) -> str | None:
    """Канонизирует жанр согласно строгим правилам медиатеки:

    1. Если жанр содержит 'rock' или 'рок' (в любой части/компоненте, case-insensitive,
       за исключением 'барокко'), он полностью схлопывается в 'Rock'.
    2. Иначе, если жанр содержит 'pop', 'поп', 'hip', 'хип', 'hip-hop', 'rap', 'рэп',
       он полностью схлопывается в 'Hip-Hop'.
    3. Для остальных жанров: переводит русскоязычные термины на английский,
       удаляет пробелы вокруг разделителей, устраняет дубликаты и склеивает через '\\\\'.
    """
    if raw is None:
        return None

    # Если передан список/множество — объединяем элементы
    if isinstance(raw, (list, tuple, set)):
        items: list[str] = []
        for el in raw:
            if el is not None:
                items.extend(re.split(r"[\x00/;,|]|\s*\\+\s*", str(el)))
        raw_str = "\\\\".join(p.strip() for p in items if p.strip())
    else:
        raw_str = str(raw).strip()

    if not raw_str:
        return None

    raw_lower = raw_str.lower()

    # 1. Проверка на Rock / Рок (барокко исключается)
    test_str = raw_lower.replace("барокко", "").replace("baroque", "")
    if ROCK_REGEX.search(test_str):
        return "Rock"

    # 2. Проверка на Hip-Hop / Pop / Rap
    if HIP_POP_RAP_REGEX.search(raw_lower):
        return "Hip-Hop"

    # 3. Остальные жанры: покомпонентный перевод и дедупликация без пробелов вокруг '\\'
    raw_lower_single = re.sub(r"\\+", r"\\", raw_lower)
    if raw_lower in RUSSIAN_GENRE_MAP:
        return RUSSIAN_GENRE_MAP[raw_lower]
    if raw_lower_single in RUSSIAN_GENRE_MAP:
        return RUSSIAN_GENRE_MAP[raw_lower_single]

    tokens = [p.strip() for p in re.split(r"[\x00/;,|]|\s*\\+\s*", raw_str) if p.strip()]
    cleaned_items: list[str] = []
    seen: set[str] = set()

    for tok in tokens:
        tok_lower = tok.lower()
        mapped = RUSSIAN_GENRE_MAP.get(tok_lower, tok)
        sub_parts = [p.strip() for p in re.split(r"\\+|\x00", mapped) if p.strip()]
        for sp in sub_parts:
            part = normalize_genre_item(sp)
            lower_key = part.lower()
            if lower_key not in seen:
                seen.add(lower_key)
                cleaned_items.append(part)

    if not cleaned_items:
        return None
    return "\\\\".join(cleaned_items)


def format_genres(raw: Any) -> str | None:
    """Нормализует жанры: разбивает по разделителям (/ ; , | \x00 \\),

    приводит Hip-Hop к каноническому виду, устраняет дубликаты без учета регистра
    и объединяет через '\\\\'.
    """
    if raw is None:
        return None
    if isinstance(raw, (list, tuple, set)):
        items: list[str] = []
        for el in raw:
            if el is not None:
                items.extend(re.split(r"[\x00/;,|]|\s*\\+\s*", str(el)))
    else:
        raw_str = str(raw).strip()
        if not raw_str:
            return None
        items = re.split(r"[\x00/;,|]|\s*\\+\s*", raw_str)

    cleaned_items: list[str] = []
    seen: set[str] = set()

    for item in items:
        part = normalize_genre_item(item)
        if not part:
            continue
        lower_key = part.lower()
        if lower_key not in seen:
            seen.add(lower_key)
            cleaned_items.append(part)

    if not cleaned_items:
        return None
    return "\\\\".join(cleaned_items)


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

    @field_validator("year", mode="before")
    @classmethod
    def _validate_album_year(cls, v: Any) -> int | None:
        if v is None or v == "":
            return None
        try:
            val = int(v)
            return val if 1000 <= val <= 2999 else None
        except (ValueError, TypeError):
            return None

    @field_validator("disc_total", mode="before")
    @classmethod
    def _validate_album_disc_total(cls, v: Any) -> int | None:
        if v is None or v == "":
            return None
        try:
            val = int(v)
            return val if val >= 1 else None
        except (ValueError, TypeError):
            return None

    @field_validator("genre", mode="before")
    @classmethod
    def _validate_genre(cls, v: Any) -> str | None:
        return format_genres(v)


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
    cover_path: str | None = Field(
        default=None,
        description="Относительный безопасный путь к файлу обложки в песочнице",
    )

    @field_validator("year", mode="before")
    @classmethod
    def _validate_track_year(cls, v: Any) -> int | None:
        if v is None or v == "":
            return None
        try:
            val = int(v)
            return val if 1000 <= val <= 2999 else None
        except (ValueError, TypeError):
            return None

    @field_validator("track_number", "disc_number", mode="before")
    @classmethod
    def _validate_positive_int_default_1(cls, v: Any) -> int:
        if v is None or v == "":
            return 1
        try:
            val = int(v)
            return max(val, 1)
        except (ValueError, TypeError):
            return 1

    @field_validator("track_total", "disc_total", mode="before")
    @classmethod
    def _validate_positive_int_optional(cls, v: Any) -> int | None:
        if v is None or v == "":
            return None
        try:
            val = int(v)
            return val if val >= 1 else None
        except (ValueError, TypeError):
            return None

    @field_validator("genre", mode="before")
    @classmethod
    def _validate_genre(cls, v: Any) -> str | None:
        return format_genres(v)


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

    source: Literal["apple", "deezer", "yandex", "spotify"] = Field(..., description="Сервис-источник")
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

    @field_validator("genre", mode="before")
    @classmethod
    def _validate_genre(cls, v: Any) -> str | None:
        return format_genres(v)


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

    @field_validator("genre", mode="before")
    @classmethod
    def _validate_genre(cls, v: Any) -> str | None:
        return format_genres(v)


