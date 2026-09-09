"""Схемы данных Pydantic для сервиса текстов песен (Lyrics Service).

Определяет модели для поиска, извлечения и сохранения текстов песен (.lrc / .txt).
"""

from typing import Literal

from pydantic import BaseModel, Field, model_validator


class LyricsSearchResult(BaseModel):
    """Результат поиска текста песни во внешних провайдерах (LRCLIB, Genius)."""

    lyrics: str | None = None
    content: str | None = None
    synced: bool = False
    source: Literal["lrclib", "genius", "none"] = "none"
    track_name: str | None = None
    artist_name: str | None = None

    @model_validator(mode="after")
    def _sync_lyrics_and_content(self) -> "LyricsSearchResult":
        if self.lyrics and not self.content:
            self.content = self.lyrics
        elif self.content and not self.lyrics:
            self.lyrics = self.content
        return self


class LyricsResponse(BaseModel):
    """Ответ с текущим состоянием текста для конкретного трека на диске."""

    track_path: str = Field(..., description="Относительный путь к целевому MP3-файлу")
    lyrics: str | None = Field(default=None, description="Содержимое текста песни")
    content: str | None = Field(default=None, description="Содержимое текста песни (алиас)")
    kind: Literal["none", "lrc", "txt"] = Field(
        default="none",
        description="Тип найденного текста (lrc — синхронизированный, txt — обычный, none — отсутствует)",
    )
    synced: bool = Field(
        default=False,
        description="Флаг наличия временных меток синхронизации",
    )

    @model_validator(mode="after")
    def _sync_lyrics_and_content(self) -> "LyricsResponse":
        if self.lyrics and not self.content:
            self.content = self.lyrics
        elif self.content and not self.lyrics:
            self.lyrics = self.content
        return self


class SaveLyricsRequest(BaseModel):
    """Запрос на сохранение текста песни для указанного трека."""

    track_path: str = Field(..., description="Относительный путь к целевому MP3-файлу")
    content: str = Field(..., description="Текст песни (.lrc или обычный .txt)")
    kind: Literal["auto", "lrc", "txt"] = Field(
        default="auto",
        description="Тип текста ('auto' — автоопределение по таймкодам, 'lrc' или 'txt')",
    )


class DeleteLyricsResponse(BaseModel):
    """Ответ на удаление текста песни."""

    success: bool = True
    deleted: bool = Field(..., description="Был ли удален хотя бы один файл текста (.lrc/.txt)")
