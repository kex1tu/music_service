"""Тесты для нативного Go-сканера медиатеки и его интеграции (Native Scanner Tests)."""

import os
import stat
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.core.sandbox import PathSandbox
from app.services.library_audit_service import LibraryAuditService
from app.services.native_scanner import NativeScannerService
from app.services.tag_service import TagService


@pytest.fixture
def test_sandbox(tmp_path: Path) -> PathSandbox:
    music_dir = tmp_path / "music"
    music_dir.mkdir(parents=True, exist_ok=True)
    return PathSandbox(music_dir)


def test_is_available_false_when_bin_missing(test_sandbox: PathSandbox, tmp_path: Path):
    missing_bin = tmp_path / "bin" / "non_existent_scanner"
    scanner = NativeScannerService(music_sandbox=test_sandbox, bin_path=missing_bin)
    assert scanner.is_available is False


def test_is_available_false_when_not_executable(test_sandbox: PathSandbox, tmp_path: Path):
    dummy_file = tmp_path / "dummy_scanner"
    dummy_file.write_text("#!/bin/sh\nexit 0\n")
    # Без прав на исполнение
    dummy_file.chmod(stat.S_IRUSR | stat.S_IWUSR)

    scanner = NativeScannerService(music_sandbox=test_sandbox, bin_path=dummy_file)
    assert scanner.is_available is False


def test_is_available_true_when_executable(test_sandbox: PathSandbox, tmp_path: Path):
    dummy_file = tmp_path / "dummy_scanner"
    dummy_file.write_text("#!/bin/sh\nexit 0\n")
    dummy_file.chmod(stat.S_IRUSR | stat.S_IWUSR | stat.S_IXUSR)

    scanner = NativeScannerService(music_sandbox=test_sandbox, bin_path=dummy_file)
    assert scanner.is_available is True


@pytest.mark.asyncio
async def test_scan_stream_parses_ndjson(test_sandbox: PathSandbox, tmp_path: Path):
    fake_scanner = tmp_path / "fake_scanner.sh"
    fake_scanner.write_text(
        '#!/bin/sh\n'
        'echo \'{"rel_path":"A/B/01. Trk1.mp3","title":"Trk1","artist":"Art1","duration":120.4,"mtime":100.0,"size":1000}\'\n'
        'echo \'{"rel_path":"A/B/02. Trk2.mp3","title":"Trk2","artist":"Art2","duration":200.8,"mtime":101.0,"size":2000}\'\n'
    )
    fake_scanner.chmod(stat.S_IRUSR | stat.S_IWUSR | stat.S_IXUSR)

    scanner = NativeScannerService(music_sandbox=test_sandbox, bin_path=fake_scanner)
    assert scanner.is_available is True

    items = []
    async for item in scanner.scan_stream():
        items.append(item)

    assert len(items) == 2
    assert items[0]["title"] == "Trk1"
    assert items[0]["duration"] == 120.4
    assert items[1]["title"] == "Trk2"
    assert items[1]["size"] == 2000


@pytest.mark.asyncio
async def test_scan_stream_skips_invalid_json_lines(test_sandbox: PathSandbox, tmp_path: Path):
    fake_scanner = tmp_path / "fake_scanner.sh"
    fake_scanner.write_text(
        '#!/bin/sh\n'
        'echo "not a valid json line"\n'
        'echo \'{"rel_path":"A/B/01. Valid.mp3","title":"Valid","artist":"Art","duration":150.0,"mtime":100.0,"size":1000}\'\n'
    )
    fake_scanner.chmod(stat.S_IRUSR | stat.S_IWUSR | stat.S_IXUSR)

    scanner = NativeScannerService(music_sandbox=test_sandbox, bin_path=fake_scanner)
    items = [item async for item in scanner.scan_stream()]

    assert len(items) == 1
    assert items[0]["title"] == "Valid"


@pytest.mark.asyncio
async def test_scan_stream_returns_empty_when_not_available(test_sandbox: PathSandbox, tmp_path: Path):
    missing_bin = tmp_path / "missing_scanner"
    scanner = NativeScannerService(music_sandbox=test_sandbox, bin_path=missing_bin)

    items = [item async for item in scanner.scan_stream()]
    assert items == []


@pytest.mark.asyncio
async def test_ensure_index_uses_native_scanner_when_available(test_sandbox: PathSandbox):
    tag_service = MagicMock(spec=TagService)
    native_scanner = MagicMock(spec=NativeScannerService)
    native_scanner.is_available = True

    async def mock_stream():
        yield {
            "rel_path": "Artist/Album/01. Song.mp3",
            "title": "Song",
            "artist": "Artist\\Feature",
            "album": "Album",
            "album_artist": "Artist",
            "year": 2024,
            "track_number": 1,
            "track_total": 10,
            "disc_number": 1,
            "disc_total": 1,
            "duration": 185.7,
            "has_cover": True,
            "lyrics_status": "lrc",
            "size": 54321,
            "mtime": 1700000000.0,
        }
        yield {
            "rel_path": "Artist/Album/02. Song 2.mp3",
            "title": "Song 2",
            "artist": "Artist",
            "album": "Album",
            "album_artist": "Artist",
            "year": 2024,
            "track_number": 2,
            "track_total": 10,
            "disc_number": 1,
            "disc_total": 1,
            "duration": 210.0,
            "has_cover": False,
            "lyrics_status": "none",
            "size": 65432,
            "mtime": 1700000001.0,
        }

    native_scanner.scan_stream = mock_stream

    audit_service = LibraryAuditService(
        music_sandbox=test_sandbox,
        tag_service=tag_service,
        native_scanner=native_scanner,
    )

    await audit_service.ensure_index()

    assert len(audit_service._cache) == 2
    cached_t1 = audit_service._cache["Artist/Album/01. Song.mp3"]
    assert cached_t1["mtime"] == 1700000000.0
    assert cached_t1["size"] == 54321
    assert cached_t1["track"].title == "Song"
    assert cached_t1["track"].artist == "Artist\\Feature"
    assert cached_t1["track"].duration == 186  # Округление до целых секунд
    assert cached_t1["track"].has_cover is True
    assert cached_t1["track"].lyrics_status == "lrc"
    assert cached_t1["track"].track_number == 1
    assert cached_t1["track"].track_total == 10

    # Проверяем, что tag_service.read_tags не вызывался (так как все взято напрямую из Go)
    tag_service.read_tags.assert_not_called()


@pytest.mark.asyncio
async def test_ensure_index_falls_back_to_python_when_native_unavailable(test_sandbox: PathSandbox):
    tag_service = MagicMock(spec=TagService)
    native_scanner = MagicMock(spec=NativeScannerService)
    native_scanner.is_available = False

    audit_service = LibraryAuditService(
        music_sandbox=test_sandbox,
        tag_service=tag_service,
        native_scanner=native_scanner,
    )

    with patch.object(audit_service, "_ensure_index_python", new_callable=AsyncMock) as mock_py:
        await audit_service.ensure_index()
        mock_py.assert_awaited_once()


@pytest.mark.asyncio
async def test_ensure_index_falls_back_to_python_when_native_fails(test_sandbox: PathSandbox):
    tag_service = MagicMock(spec=TagService)
    native_scanner = MagicMock(spec=NativeScannerService)
    native_scanner.is_available = True

    async def failing_stream():
        if False:
            yield {}
        raise RuntimeError("Process crashed")

    native_scanner.scan_stream = failing_stream

    audit_service = LibraryAuditService(
        music_sandbox=test_sandbox,
        tag_service=tag_service,
        native_scanner=native_scanner,
    )

    with patch.object(audit_service, "_ensure_index_python", new_callable=AsyncMock) as mock_py:
        await audit_service.ensure_index()
        mock_py.assert_awaited_once()


@pytest.mark.asyncio
async def test_real_compiled_go_scanner(test_sandbox: PathSandbox):
    bin_path = Path(__file__).resolve().parent.parent / "bin" / "music-scanner"
    if not bin_path.exists() or not os.access(bin_path, os.X_OK):
        pytest.skip("Скомпилированный бинарник bin/music-scanner отсутствует")

    scanner = NativeScannerService(music_sandbox=test_sandbox, bin_path=bin_path)
    assert scanner.is_available is True

    # Сканируем пустую песочницу
    items = [item async for item in scanner.scan_stream()]
    assert items == []
