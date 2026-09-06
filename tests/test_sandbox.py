"""Модульные тесты компонента изоляции файловой системы PathSandbox."""

import os
from pathlib import Path

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from app.config import Settings
from app.core.exceptions import PathTraversalError, StorageConfigurationError
from app.core.sandbox import PathSandbox
from app.dependencies import (
    MusicSandboxDep,
    TmpSandboxDep,
    get_settings,
)


def test_sandbox_initialization_valid(tmp_path: Path) -> None:
    """Проверяет успешную инициализацию с существующей директорией."""
    sandbox = PathSandbox(tmp_path)
    assert sandbox.root == tmp_path.resolve()
    assert sandbox.root.is_absolute()


def test_sandbox_initialization_empty_raises() -> None:
    """Проверяет возбуждение ошибки при пустом пути корня."""
    with pytest.raises(StorageConfigurationError, match="не может быть пустым"):
        PathSandbox("")


def test_sandbox_initialization_file_raises(tmp_path: Path) -> None:
    """Проверяет возбуждение ошибки, если передан путь к файлу вместо директории."""
    file_path = tmp_path / "not_a_dir.txt"
    file_path.write_text("content", encoding="utf-8")

    with pytest.raises(StorageConfigurationError, match="не является директорией"):
        PathSandbox(file_path)


def test_resolve_normal_relative_path(music_sandbox: PathSandbox) -> None:
    """Проверяет нормальное разрешение относительного пути."""
    resolved = music_sandbox.resolve("Daft Punk/Discovery/01. One More Time.mp3")
    expected = music_sandbox.root / "Daft Punk" / "Discovery" / "01. One More Time.mp3"
    assert resolved == expected
    assert resolved.is_relative_to(music_sandbox.root)


def test_resolve_with_path_object(music_sandbox: PathSandbox) -> None:
    """Проверяет возможность передачи объекта pathlib.Path."""
    rel_path = Path("Rock") / "Queen" / "Bohemian Rhapsody.mp3"
    resolved = music_sandbox.resolve(rel_path)
    assert resolved == music_sandbox.root / "Rock" / "Queen" / "Bohemian Rhapsody.mp3"


def test_resolve_none_raises(music_sandbox: PathSandbox) -> None:
    """Проверяет, что передача None вызывает PathTraversalError."""
    with pytest.raises(PathTraversalError, match="Путь не может быть пустым"):
        music_sandbox.resolve(None)  # type: ignore[arg-type]


def test_resolve_root_access(music_sandbox: PathSandbox) -> None:
    """Проверяет доступ к корню при allow_root=True и allow_root=False."""
    # При allow_root=True возвращается сам корень
    assert music_sandbox.resolve("", allow_root=True) == music_sandbox.root
    assert music_sandbox.resolve(".", allow_root=True) == music_sandbox.root

    # При allow_root=False выбрасывается исключение
    with pytest.raises(PathTraversalError, match="Доступ к корню песочницы запрещен"):
        music_sandbox.resolve("", allow_root=False)

    with pytest.raises(PathTraversalError, match="Доступ к корню песочницы запрещен"):
        music_sandbox.resolve(".", allow_root=False)

    with pytest.raises(PathTraversalError, match="Доступ к корню песочницы запрещен"):
        music_sandbox.resolve("subdir/..", allow_root=False)


def test_resolve_child_convenience(music_sandbox: PathSandbox) -> None:
    """Проверяет хелпер resolve_child, запрещающий доступ к корню."""
    with pytest.raises(PathTraversalError, match="Доступ к корню песочницы запрещен"):
        music_sandbox.resolve_child("")

    resolved = music_sandbox.resolve_child("track.mp3")
    assert resolved == music_sandbox.root / "track.mp3"


def test_resolve_non_existent_file_for_writing(music_sandbox: PathSandbox) -> None:
    """Проверяет разрешение еще не созданного файла (для сценариев записи)."""
    target = music_sandbox.resolve("New Artist/New Album/01. New Track.mp3")
    assert target == music_sandbox.root / "New Artist" / "New Album" / "01. New Track.mp3"
    assert not target.exists()


@pytest.mark.parametrize(
    "attack_path",
    [
        "../secret.txt",
        "../../etc/passwd",
        "folder/../../../etc/shadow",
        "a/b/../../../../../../etc/hosts",
    ],
)
def test_path_traversal_parent_escape_blocked(
    music_sandbox: PathSandbox, attack_path: str
) -> None:
    """Проверяет блокировку попыток выхода через относительные переходы (../)."""
    with pytest.raises(PathTraversalError, match="Попытка выхода за пределы песочницы"):
        music_sandbox.resolve(attack_path)


def test_path_traversal_absolute_paths_kept_safe(music_sandbox: PathSandbox) -> None:
    """Проверяет защиту от ловушки абсолютных путей в pathlib.Path."""
    # Ведущий слэш отсекается, поэтому путь безопасно резолвится внутри корня
    resolved = music_sandbox.resolve("/etc/passwd")
    assert resolved == music_sandbox.root / "etc" / "passwd"
    assert resolved.is_relative_to(music_sandbox.root)

    resolved_double_slash = music_sandbox.resolve("//var/log/syslog")
    assert resolved_double_slash == music_sandbox.root / "var" / "log" / "syslog"
    assert resolved_double_slash.is_relative_to(music_sandbox.root)


def test_null_byte_injection_blocked(music_sandbox: PathSandbox) -> None:
    """Проверяет блокировку инъекций нулевого байта (Null-byte injection)."""
    with pytest.raises(PathTraversalError, match="Обнаружен нулевой байт"):
        music_sandbox.resolve("track.mp3\0.exe")

    with pytest.raises(PathTraversalError, match="Обнаружен нулевой байт"):
        music_sandbox.resolve("\0../../etc/passwd")

    with pytest.raises(PathTraversalError, match="Обнаружен нулевой байт после URL-декодирования"):
        music_sandbox.resolve("track.mp3%00.exe")


@pytest.mark.parametrize(
    "encoded_attack",
    [
        "%2e%2e%2fetc%2fpasswd",
        "%2e%2e/secret.txt",
        "subdir/%2e%2e/%2e%2e/root_leak",
        "%252e%252e%252fetc%252fpasswd",  # Двойное URL-кодирование
    ],
)
def test_url_encoded_traversal_blocked(
    music_sandbox: PathSandbox, encoded_attack: str
) -> None:
    """Проверяет блокировку одинарного и двойного URL-кодирования path traversal."""
    with pytest.raises(PathTraversalError, match="Попытка выхода за пределы песочницы"):
        music_sandbox.resolve(encoded_attack)


def test_windows_separator_normalization(music_sandbox: PathSandbox) -> None:
    """Проверяет нормализацию обратных слэшей Windows."""
    # Легитимный путь с обратными слэшами
    resolved = music_sandbox.resolve("Artist\\Album\\Track.mp3")
    assert resolved == music_sandbox.root / "Artist" / "Album" / "Track.mp3"

    # Попытка атаки с обратными слэшами
    with pytest.raises(PathTraversalError, match="Попытка выхода за пределы песочницы"):
        music_sandbox.resolve("..\\..\\etc\\passwd")


def test_symlink_internal_allowed(music_sandbox: PathSandbox) -> None:
    """Проверяет, что внутренняя символическая ссылка внутри корня разрешена."""
    real_dir = music_sandbox.root / "real_dir"
    real_dir.mkdir()
    (real_dir / "track.mp3").write_text("audio", encoding="utf-8")

    link_dir = music_sandbox.root / "link_dir"
    os.symlink(real_dir, link_dir)

    resolved = music_sandbox.resolve("link_dir/track.mp3")
    assert resolved == real_dir / "track.mp3"
    assert resolved.is_relative_to(music_sandbox.root)


def test_symlink_external_blocked(music_sandbox: PathSandbox, tmp_path: Path) -> None:
    """Проверяет блокировку символической ссылки, указывающей во внешний каталог."""
    external_dir = tmp_path / "outside_sandbox"
    external_dir.mkdir()
    external_file = external_dir / "secret.txt"
    external_file.write_text("secret_data", encoding="utf-8")

    # Создаем симлинк внутри песочницы, указывающий наружу
    evil_link = music_sandbox.root / "evil_link"
    os.symlink(external_file, evil_link)

    with pytest.raises(PathTraversalError, match="Попытка выхода за пределы песочницы"):
        music_sandbox.resolve("evil_link")


def test_symlink_directory_external_traversal_blocked(
    music_sandbox: PathSandbox, tmp_path: Path
) -> None:
    """Проверяет блокировку прохода через симлинк-директорию наружу."""
    external_dir = tmp_path / "outside_dir"
    external_dir.mkdir()
    (external_dir / "confidential.txt").write_text("confidential", encoding="utf-8")

    evil_dir_link = music_sandbox.root / "evil_dir_link"
    os.symlink(external_dir, evil_dir_link)

    with pytest.raises(PathTraversalError, match="Попытка выхода за пределы песочницы"):
        music_sandbox.resolve("evil_dir_link/confidential.txt")


def test_to_relative_success(music_sandbox: PathSandbox) -> None:
    """Проверяет корректную обратную конвертацию пути в относительный вид."""
    abs_path = music_sandbox.root / "Daft Punk" / "Discovery" / "01.mp3"
    rel = music_sandbox.to_relative(abs_path)
    assert rel == Path("Daft Punk/Discovery/01.mp3")


def test_to_relative_outside_raises(music_sandbox: PathSandbox, tmp_path: Path) -> None:
    """Проверяет исключение при попытке получить относительный путь внешнего объекта."""
    outside_path = tmp_path / "outside.mp3"
    with pytest.raises(PathTraversalError, match="не принадлежит корню песочницы"):
        music_sandbox.to_relative(outside_path)


def test_is_safe(music_sandbox: PathSandbox) -> None:
    """Проверяет булев метод is_safe."""
    assert music_sandbox.is_safe("valid/artist/track.mp3") is True
    assert music_sandbox.is_safe("../../etc/passwd") is False
    assert music_sandbox.is_safe("track.mp3\0.exe") is False
    assert music_sandbox.is_safe("", allow_root=False) is False
    assert music_sandbox.is_safe("", allow_root=True) is True


def test_validate_filename_valid() -> None:
    """Проверяет валидацию корректного имени файла."""
    valid_names = [
        "01. Track Name.mp3",
        "Artist - Album (2024).flac",
        "cover.jpg",
        "lyrics.lrc",
    ]
    for name in valid_names:
        assert PathSandbox.validate_filename(name) == name


@pytest.mark.parametrize(
    "invalid_name",
    [
        "",
        ".",
        "..",
        "path/to/file.mp3",
        "path\\to\\file.mp3",
        "song:title.mp3",
        "song*title.mp3",
        "song?title.mp3",
        'song"title.mp3',
        "song<title.mp3",
        "song>title.mp3",
        "song|title.mp3",
        "song\0title.mp3",
    ],
)
def test_validate_filename_invalid(invalid_name: str) -> None:
    """Проверяет возбуждение PathTraversalError для некорректных имен файлов."""
    with pytest.raises(PathTraversalError):
        PathSandbox.validate_filename(invalid_name)


def test_fastapi_dependency_injection(test_settings: Settings) -> None:
    """Проверяет работу зависимостей get_music_sandbox и get_tmp_sandbox в FastAPI."""
    app = FastAPI()
    app.dependency_overrides[get_settings] = lambda: test_settings

    @app.get("/test/music-sandbox")
    def music_route(sandbox: MusicSandboxDep) -> dict[str, str]:
        resolved = sandbox.resolve("test.mp3")
        return {"root": str(sandbox.root), "resolved": str(resolved)}

    @app.get("/test/tmp-sandbox")
    def tmp_route(sandbox: TmpSandboxDep) -> dict[str, str]:
        resolved = sandbox.resolve("job123/raw/stream.mp3")
        return {"root": str(sandbox.root), "resolved": str(resolved)}

    client = TestClient(app)

    response_music = client.get("/test/music-sandbox")
    assert response_music.status_code == 200
    data_music = response_music.json()
    assert data_music["root"] == str(test_settings.MUSIC_DIR)
    assert data_music["resolved"] == str(test_settings.MUSIC_DIR / "test.mp3")

    response_tmp = client.get("/test/tmp-sandbox")
    assert response_tmp.status_code == 200
    data_tmp = response_tmp.json()
    assert data_tmp["root"] == str(test_settings.TMP_DIR)
    assert data_tmp["resolved"] == str(test_settings.TMP_DIR / "job123" / "raw" / "stream.mp3")
