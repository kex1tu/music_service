"""Pydantic-схемы данных для подсистемы файлового менеджера (File Manager Service).

Определяет структуры представления элементов каталога (папок и аудиофайлов),
навигационных цепочек (breadcrumbs) и ответов на операции листинга и удаления.
"""

from typing import Literal

from pydantic import BaseModel, Field


class FileItem(BaseModel):
    """Элемент файлового дерева (каталог или файл с базовыми метаданными)."""

    name: str = Field(..., description="Имя файла или каталога")
    path: str = Field(..., description="Относительный путь внутри MUSIC_DIR")
    type: Literal["directory", "file"] = Field(..., description="Тип элемента (папка или файл)")
    size: int | None = Field(default=None, description="Размер файла в байтах")
    extension: str | None = Field(default=None, description="Расширение файла (например, .mp3)")
    track_count: int | None = Field(
        default=None, description="Количество треков внутри каталога (для папок)"
    )

    # Метаданные для MP3-файлов
    track_number: int | None = Field(default=None, description="Номер дорожки на диске")
    title: str | None = Field(default=None, description="Название композиции")
    artist: str | None = Field(default=None, description="Исполнитель трека")
    album: str | None = Field(default=None, description="Название альбома")
    duration: int | None = Field(default=None, description="Длительность звучания в секундах")
    has_cover: bool = Field(default=False, description="Наличие встроенной обложки (APIC)")
    lyrics_status: Literal["none", "lrc", "txt"] = Field(
        default="none", description="Статус наличия файла текста песни"
    )


class BreadcrumbItem(BaseModel):
    """Элемент навигационной цепочки хлебных крошек."""

    name: str = Field(..., description="Отображаемое имя сегмента пути")
    path: str = Field(..., description="Относительный путь для перехода")


class DirectoryListingResponse(BaseModel):
    """Ответ на запрос содержимого каталога."""

    current_path: str = Field(default="", description="Текущий относительный путь")
    parent_path: str | None = Field(
        default=None, description="Относительный путь к родительскому каталогу"
    )
    breadcrumbs: list[BreadcrumbItem] = Field(
        default_factory=list, description="Навигационная цепочка пути"
    )
    items: list[FileItem] = Field(default_factory=list, description="Список элементов каталога")
    total_directories: int = Field(default=0, description="Количество подпапок в каталоге")
    total_files: int = Field(default=0, description="Количество файлов в каталоге")


class DeleteFileResponse(BaseModel):
    """Ответ на операцию удаления файла или каталога."""

    success: bool = Field(default=True, description="Флаг успешности удаления")
    deleted_path: str = Field(..., description="Относительный путь удаленного объекта")
    is_directory: bool = Field(
        default=False, description="Являлся ли удаленный объект каталогом"
    )
