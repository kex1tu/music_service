"""Модульные тесты для сервиса файлового менеджера FileManagerService."""

from pathlib import Path

import pytest

from app.core.exceptions import NotFoundError, PathTraversalError
from app.core.sandbox import PathSandbox
from app.schemas.tag import TrackTags
from app.services.file_manager_service import FileManagerService
from app.services.tag_service import TagService

MINIMAL_MP3 = b"\xff\xfb\x90\x00" + b"\x00" * 300


@pytest.fixture
def file_manager(temp_storage: dict[str, Path]) -> tuple[FileManagerService, TagService, Path]:
    """Фикстура сервиса FileManagerService с песочницей медиатеки."""
    music_dir = temp_storage["music_dir"]
    sandbox = PathSandbox(music_dir)
    tag_service = TagService(music_sandbox=sandbox)
    fm = FileManagerService(music_sandbox=sandbox, tag_service=tag_service)
    return fm, tag_service, music_dir


def test_list_dir_empty_root(file_manager: tuple[FileManagerService, TagService, Path]):
    """Проверка листинга пустого корня медиатеки."""
    fm, _, _ = file_manager
    res = fm.list_dir("")
    assert res.current_path == ""
    assert res.parent_path is None
    assert len(res.items) == 0
    assert res.total_directories == 0
    assert res.total_files == 0
    assert len(res.breadcrumbs) == 1
    assert res.breadcrumbs[0].name == "Медиатека"


def test_list_dir_with_folders_and_files(file_manager: tuple[FileManagerService, TagService, Path]):
    """Проверка листинга директории с подпапками и MP3-файлами."""
    fm, tag_svc, music_dir = file_manager

    # Создаем структуру: Папка артиста и трек в корне
    artist_dir = music_dir / "Daft Punk"
    artist_dir.mkdir(parents=True, exist_ok=True)
    album_dir = artist_dir / "Discovery - 2001"
    album_dir.mkdir(parents=True, exist_ok=True)

    t1 = album_dir / "01. One More Time.mp3"
    t1.write_bytes(MINIMAL_MP3)
    tag_svc.write_tags(
        t1,
        TrackTags(
            path="Daft Punk/Discovery - 2001/01. One More Time.mp3",
            track_number=1,
            title="One More Time",
            artist="Daft Punk",
            album="Discovery",
            year=2001,
        ),
    )

    # Листинг корня
    root_res = fm.list_dir("")
    assert root_res.total_directories == 1
    assert root_res.total_files == 0
    assert root_res.items[0].name == "Daft Punk"
    assert root_res.items[0].type == "directory"
    assert root_res.items[0].path == "Daft Punk"

    # Листинг папки альбома
    album_res = fm.list_dir("Daft Punk/Discovery - 2001")
    assert album_res.current_path == "Daft Punk/Discovery - 2001"
    assert album_res.parent_path == "Daft Punk"
    assert album_res.total_files == 1
    track_item = album_res.items[0]
    assert track_item.name == "01. One More Time.mp3"
    assert track_item.type == "file"
    assert track_item.title == "One More Time"
    assert track_item.artist == "Daft Punk"
    assert track_item.track_number == 1
    assert len(album_res.breadcrumbs) == 3


def test_list_dir_lyrics_status(file_manager: tuple[FileManagerService, TagService, Path]):
    """Проверка определения наличия текстов песен (.lrc/.txt) при листинге."""
    fm, tag_svc, music_dir = file_manager

    t1 = music_dir / "01. Test.mp3"
    t1.write_bytes(MINIMAL_MP3)
    lrc1 = music_dir / "01. Test.lrc"
    lrc1.write_text("[00:01.00] Synced line", encoding="utf-8")

    tag_svc.write_tags(t1, TrackTags(path="01. Test.mp3", title="Test"))

    res = fm.list_dir("")
    assert res.total_files == 2  # mp3 + lrc
    mp3_item = next(i for i in res.items if i.name == "01. Test.mp3")
    assert mp3_item.lyrics_status == "lrc"


def test_list_dir_not_found(file_manager: tuple[FileManagerService, TagService, Path]):
    """Проверка исключения NotFoundError при запросе несуществующего каталога."""
    fm, _, _ = file_manager
    with pytest.raises(NotFoundError):
        fm.list_dir("nonexistent_folder")


def test_list_dir_file_as_dir_raises_not_found(file_manager: tuple[FileManagerService, TagService, Path]):
    """Проверка исключения NotFoundError при передаче файла вместо каталога."""
    fm, _, music_dir = file_manager
    test_file = music_dir / "song.mp3"
    test_file.write_bytes(MINIMAL_MP3)

    with pytest.raises(NotFoundError):
        fm.list_dir("song.mp3")


def test_list_dir_path_traversal(file_manager: tuple[FileManagerService, TagService, Path]):
    """Проверка блокировки Path Traversal при листинге."""
    fm, _, _ = file_manager
    with pytest.raises(PathTraversalError):
        fm.list_dir("../../etc")


def test_get_album_tracks(file_manager: tuple[FileManagerService, TagService, Path]):
    """Проверка метода get_album_tracks для альбома и одиночного файла."""
    fm, tag_svc, music_dir = file_manager

    album_dir = music_dir / "Artist" / "Album"
    album_dir.mkdir(parents=True, exist_ok=True)
    t1 = album_dir / "01. First.mp3"
    t2 = album_dir / "02. Second.mp3"
    t1.write_bytes(MINIMAL_MP3)
    t2.write_bytes(MINIMAL_MP3)

    tag_svc.write_tags(t1, TrackTags(path="Artist/Album/01. First.mp3", track_number=1, title="First"))
    tag_svc.write_tags(t2, TrackTags(path="Artist/Album/02. Second.mp3", track_number=2, title="Second"))

    tracks = fm.get_album_tracks("Artist/Album")
    assert len(tracks) == 2
    assert tracks[0].title == "First"
    assert tracks[1].title == "Second"

    single_track = fm.get_album_tracks("Artist/Album/01. First.mp3")
    assert len(single_track) == 1
    assert single_track[0].title == "First"


def test_delete_file_and_associated_lyrics(file_manager: tuple[FileManagerService, TagService, Path]):
    """Проверка безопасного удаления MP3 и прилежащего файла текста."""
    fm, _, music_dir = file_manager

    mp3_file = music_dir / "song.mp3"
    lrc_file = music_dir / "song.lrc"
    mp3_file.write_bytes(MINIMAL_MP3)
    lrc_file.write_text("lyrics", encoding="utf-8")

    res = fm.delete("song.mp3")
    assert res.success is True
    assert res.is_directory is False
    assert not mp3_file.exists()
    assert not lrc_file.exists()


def test_delete_directory(file_manager: tuple[FileManagerService, TagService, Path]):
    """Проверка удаления директории со всем содержимым."""
    fm, _, music_dir = file_manager

    folder = music_dir / "ToDelete"
    folder.mkdir(parents=True, exist_ok=True)
    (folder / "file.mp3").write_bytes(MINIMAL_MP3)

    res = fm.delete("ToDelete")
    assert res.success is True
    assert res.is_directory is True
    assert not folder.exists()


def test_delete_root_forbidden(file_manager: tuple[FileManagerService, TagService, Path]):
    """Инвариант: удаление корня медиатеки строго запрещено."""
    fm, _, _ = file_manager

    with pytest.raises(PathTraversalError):
        fm.delete("")

    with pytest.raises(PathTraversalError):
        fm.delete("/")

    with pytest.raises(PathTraversalError):
        fm.delete(".")


def test_delete_not_found(file_manager: tuple[FileManagerService, TagService, Path]):
    """Проверка удаления несуществующего файла."""
    fm, _, _ = file_manager
    with pytest.raises(NotFoundError):
        fm.delete("not_here.mp3")


def test_delete_none_raises_path_traversal(file_manager: tuple[FileManagerService, TagService, Path]):
    """Проверка запрета удаления None (защита от неявного преобразования в строку 'None')."""
    fm, _, _ = file_manager
    with pytest.raises(PathTraversalError):
        fm.delete(None)  # type: ignore


def test_list_dir_artist_folder_recursive_track_count(
    file_manager: tuple[FileManagerService, TagService, Path],
):
    """Проверка подсчета треков во вложенных альбомах для каталога артиста."""
    fm, _, music_dir = file_manager

    # Создаем папку артиста, в ней 2 альбома с треками
    artist_dir = music_dir / "Pink Floyd"
    album1 = artist_dir / "The Wall - 1979"
    album2 = artist_dir / "Dark Side - 1973"
    album1.mkdir(parents=True, exist_ok=True)
    album2.mkdir(parents=True, exist_ok=True)

    (album1 / "01. In the Flesh.mp3").write_bytes(MINIMAL_MP3)
    (album1 / "02. The Thin Ice.mp3").write_bytes(MINIMAL_MP3)
    (album2 / "01. Speak to Me.mp3").write_bytes(MINIMAL_MP3)

    res = fm.list_dir("")
    assert res.total_directories == 1
    item = res.items[0]
    assert item.name == "Pink Floyd"
    assert item.track_count == 3


def test_list_dir_ignores_hidden_files_and_directories(
    file_manager: tuple[FileManagerService, TagService, Path],
):
    """Скрытые файлы (.DS_Store, .probe_...) и директории отсеиваются из листинга."""
    fm, _, music_dir = file_manager

    # Создаем обычный файл и скрытые служебные файлы/папки
    (music_dir / "track.mp3").write_bytes(MINIMAL_MP3)
    (music_dir / ".DS_Store").write_bytes(b"garbage")
    (music_dir / ".probe_test.tmp").write_bytes(b"probe")
    (music_dir / ".hidden_folder").mkdir()

    res = fm.list_dir("")
    assert res.total_files == 1
    assert res.total_directories == 0
    assert len(res.items) == 1
    assert res.items[0].name == "track.mp3"

