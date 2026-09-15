"""Pydantic-схемы данных для подсистемы метаданных и тегов (Metadata & Tag Service).

Определяет структуры метаданных треков и альбомов, запросов пакетного сохранения
и ответов API для редактора тегов.
"""

import re
from typing import Any, Literal

from pydantic import BaseModel, Field, field_validator

CYRILLIC_GENRE_REGEX = re.compile(r"[\u0400-\u04FF]")

RUSSIAN_GENRE_MAP: dict[str, str] = {
    "поп\\рок": "Pop\\\\Rock",
    "поп/рок": "Pop\\\\Rock",
    "поп-рок": "Pop-Rock",
    "поп рок": "Pop-Rock",
    "альтернативная музыка": "Alternative",
    "альтернатива": "Alternative",
    "танцевальная музыка": "Dance Music",
    "танцевальная": "Dance Music",
    "русский рэп": "Russian Hip-Hop",
    "рэп": "Rap",
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

GENRE_CANONICAL_NAMES: dict[str, str] = {
    "hip-hop": "Hip-Hop",
    "hip hop": "Hip-Hop",
    "hiphop": "Hip-Hop",
    "rap": "Rap",
    "rock": "Rock",
    "pop": "Pop",
    "alternative": "Alternative",
    "alternative rock": "Alternative Rock",
    "indie": "Indie",
    "indie rock": "Indie Rock",
    "pop-rock": "Pop-Rock",
    "pop rock": "Pop-Rock",
    "hard rock": "Hard Rock",
    "punk": "Punk",
    "punk rock": "Punk Rock",
    "metal": "Metal",
    "heavy metal": "Heavy Metal",
    "jazz": "Jazz",
    "blues": "Blues",
    "electronic": "Electronic",
    "dance": "Dance",
    "dance music": "Dance Music",
    "house": "House",
    "techno": "Techno",
    "trance": "Trance",
    "dubstep": "Dubstep",
    "drum and bass": "Drum and Bass",
    "drum & bass": "Drum and Bass",
    "dnb": "Drum and Bass",
    "phonk": "Phonk",
    "drill": "Drill",
    "classical": "Classical",
    "soundtrack": "Soundtrack",
    "ambient": "Ambient",
    "disco": "Disco",
    "reggae": "Reggae",
    "chanson": "Chanson",
    "folk": "Folk",
    "soul": "Soul",
    "funk": "Funk",
    "r&b": "R&B",
    "rnb": "R&B",
    "r and b": "R&B",
    "k-pop": "K-Pop",
    "kpop": "K-Pop",
    "j-pop": "J-Pop",
    "jpop": "J-Pop",
    "lo-fi": "Lo-Fi",
    "lofi": "Lo-Fi",
    "edm": "EDM",
    "synthpop": "Synthpop",
    "synth-pop": "Synthpop",
    "russian rock": "Russian Rock",
    "russian rap": "Russian Rap",
    "russian hip-hop": "Russian Hip-Hop",
    "russian pop": "Russian Pop",
    "russian chanson": "Russian Chanson",
}


def normalize_genre_item(item: str) -> str:
    """Нормализует отдельный жанр, приводя 'Hip-Hop' к каноническому виду через дефис."""
    cleaned = item.strip()
    if not cleaned:
        return ""
    # Hip-Hop строго пишется как 'Hip-Hop' (через дефис)
    return re.sub(r"(?i)\b(?:hip[- ]?hop|хип[- ]?хоп|hiphop)\b", "Hip-Hop", cleaned)


def normalize_canonical_genre_item(item: str) -> str:
    """Нормализует отдельный жанр для канонизации: канонический словарь, Hip-Hop и Title Case."""
    cleaned = item.strip()
    if not cleaned:
        return ""
    lower = cleaned.lower()
    if lower in GENRE_CANONICAL_NAMES:
        return GENRE_CANONICAL_NAMES[lower]
    replaced = re.sub(r"(?i)\b(?:hip[- ]?hop|хип[- ]?хоп|hiphop)\b", "Hip-Hop", cleaned)
    if replaced != cleaned:
        return replaced
    if cleaned.islower() and not CYRILLIC_GENRE_REGEX.search(cleaned):
        words = re.split(r"(\s+|-)", cleaned)
        return "".join(w.capitalize() if w.isalnum() else w for w in words)
    return cleaned


def canonicalize_genre(raw: Any) -> str | None:
    """Канонизирует жанр согласно строгим правилам медиатеки:

    1. Если передано несколько жанров через разделители (\\\\, /, ;, ,, |, \\x00),
       каждый жанр рассматривается индивидуально и НЕ схлопывается в один общий жанр.
       Например: 'Rap\\\\Hip-Hop' сохраняется как 'Rap\\\\Hip-Hop'.
    2. Каждый отдельный компонент нормализуется:
       - 'Hip hop' / 'hip-hop' / 'hiphop' -> 'Hip-Hop';
       - русскоязычные жанры переводятся на английский ('Рэп' -> 'Rap', 'Рок' -> 'Rock' и т.д.);
       - исправляется регистр и написание ('rock' -> 'Rock', 'pop' -> 'Pop');
    3. Исключаются любые пробелы вокруг разделителей '\\\\'.
    4. Дубликаты устраняются без учета регистра с сохранением исходного порядка.
    5. Компоненты объединяются строго через '\\\\'.
    """
    if raw is None:
        return None

    # Если передан список/множество — объединяем элементы
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
        tok = item.strip()
        if not tok:
            continue
        tok_lower = tok.lower()
        if tok_lower in RUSSIAN_GENRE_MAP:
            mapped = RUSSIAN_GENRE_MAP[tok_lower]
            sub_parts = re.split(r"[\x00/;,|]|\s*\\+\s*", mapped)
        else:
            sub_parts = [tok]

        for sp in sub_parts:
            part = normalize_canonical_genre_item(sp)
            if not part:
                continue
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


