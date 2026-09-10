"""Модульные тесты сервиса торрентов (TorrentService и вспомогательные функции)."""

from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.config import Settings
from app.core.exceptions import TorrentError
from app.services.torrent_service import (
    ARIA2C_PROGRESS_REGEX,
    CueSheetParser,
    CueTrack,
    TorrentService,
    _parse_size_to_bytes,
    discover_audio_files,
    extract_tags_from_file,
    find_cover_art,
)


def test_aria2c_progress_regex():
    """Тестирование регулярного выражения для разбора вывода aria2c."""
    line = "[#2089b0 45.5MiB/1.2GiB(3%) CN:12 DL:5.2MiB ETA:3m2s]"
    match = ARIA2C_PROGRESS_REGEX.search(line)
    assert match is not None
    assert match.group("dl_size") == "45.5MiB"
    assert match.group("total_size") == "1.2GiB"
    assert match.group("percent") == "3"
    assert match.group("connections") == "12"
    assert match.group("speed") == "5.2MiB"
    assert match.group("eta") == "3m2s"


def test_aria2c_progress_regex_without_eta():
    """Тестирование парсинга строки без ETA."""
    line = "[#abc123 100MiB/100MiB(100%) CN:0 DL:0B]"
    match = ARIA2C_PROGRESS_REGEX.search(line)
    assert match is not None
    assert match.group("percent") == "100"
    assert match.group("connections") == "0"
    assert match.group("eta") is None


def test_parse_size_to_bytes():
    """Тестирование преобразования строк размера в байты."""
    assert _parse_size_to_bytes("1.2GiB") == int(1.2 * 1024**3)
    assert _parse_size_to_bytes("50MiB") == 50 * 1024**2
    assert _parse_size_to_bytes("500KiB") == 500 * 1024
    assert _parse_size_to_bytes("100B") == 100
    assert _parse_size_to_bytes("0B") == 0
    assert _parse_size_to_bytes("invalid") == 0


def test_cue_parser_basic(tmp_path: Path):
    """Тестирование парсинга CUE-файла."""
    cue_content = """
    PERFORMER "Daft Punk"
    TITLE "Discovery"
    FILE "Daft Punk - Discovery.flac" WAVE
      TRACK 01 AUDIO
        TITLE "One More Time"
        PERFORMER "Daft Punk"
        INDEX 01 00:00:00
      TRACK 02 AUDIO
        TITLE "Aerodynamic"
        PERFORMER "Daft Punk"
        INDEX 01 05:20:35
      TRACK 03 AUDIO
        TITLE "Digital Love"
        INDEX 01 08:52:60
    """
    cue_file = tmp_path / "album.cue"
    cue_file.write_text(cue_content, encoding="utf-8")

    tracks = CueSheetParser.parse(cue_file)
    assert len(tracks) == 3

    assert tracks[0].number == 1
    assert tracks[0].title == "One More Time"
    assert tracks[0].performer == "Daft Punk"
    assert tracks[0].start_seconds == 0.0
    assert tracks[0].end_seconds == pytest.approx(5 * 60 + 20 + 35 / 75.0, 0.01)

    assert tracks[1].number == 2
    assert tracks[1].title == "Aerodynamic"
    assert tracks[1].start_seconds == pytest.approx(5 * 60 + 20 + 35 / 75.0, 0.01)
    assert tracks[1].end_seconds == pytest.approx(8 * 60 + 52 + 60 / 75.0, 0.01)

    assert tracks[2].number == 3
    assert tracks[2].title == "Digital Love"
    assert tracks[2].performer == "Daft Punk"  # Унаследовано от global
    assert tracks[2].end_seconds is None


def test_cue_parser_windows1251(tmp_path: Path):
    """Тестирование парсинга CUE-файла в кодировке Windows-1251 (русские теги)."""
    cue_content = """
    PERFORMER "Кино"
    TITLE "Группа крови"
    FILE "kino.flac" WAVE
      TRACK 01 AUDIO
        TITLE "Группа крови"
        INDEX 01 00:00:00
    """
    cue_file = tmp_path / "kino.cue"
    cue_file.write_bytes(cue_content.encode("windows-1251"))

    tracks = CueSheetParser.parse(cue_file)
    assert len(tracks) == 1
    assert tracks[0].title == "Группа крови"
    assert tracks[0].performer == "Кино"


@pytest.mark.asyncio
async def test_cue_split_audio_success(tmp_path: Path):
    """Тестирование сплиттинга аудио через ffmpeg subprocess."""
    audio_file = tmp_path / "big_album.flac"
    audio_file.write_bytes(b"FLAC_DATA")

    tracks = [
        CueTrack(number=1, title="Track 1", performer="Artist", start_seconds=0.0, end_seconds=60.0),
        CueTrack(number=2, title="Track 2", performer="Artist", start_seconds=60.0, end_seconds=None),
    ]

    out_dir = tmp_path / "split"
    out_dir.mkdir()

    # Мокаем успешный запуск ffmpeg, создавая целевые файлы
    async def fake_proc(*args, **kwargs):
        # Находим имя выходного файла из аргументов cmd
        out_arg = args[-1]
        Path(out_arg).write_bytes(b"SPLIT_TRACK")
        proc_mock = MagicMock()
        proc_mock.returncode = 0
        proc_mock.communicate = AsyncMock(return_value=(b"", b""))
        return proc_mock

    with patch("asyncio.create_subprocess_exec", side_effect=fake_proc):
        split_files = await CueSheetParser.split_audio(audio_file, tracks, out_dir)
        assert len(split_files) == 2
        assert split_files[0].name == "01. Track 1.flac"
        assert split_files[1].name == "02. Track 2.flac"


def test_discover_audio_files(tmp_path: Path):
    """Тестирование обнаружения только поддерживаемых аудиоформатов."""
    (tmp_path / "track1.flac").write_bytes(b"")
    (tmp_path / "track2.mp3").write_bytes(b"")
    (tmp_path / "track3.m4a").write_bytes(b"")
    (tmp_path / "album.cue").write_bytes(b"")
    (tmp_path / "album.log").write_bytes(b"")
    (tmp_path / "cover.jpg").write_bytes(b"")
    (tmp_path / "info.nfo").write_bytes(b"")

    sub_dir = tmp_path / "sub"
    sub_dir.mkdir()
    (sub_dir / "track4.wav").write_bytes(b"")

    discovered = discover_audio_files(tmp_path)
    discovered_names = [f.name for f in discovered]

    assert "track1.flac" in discovered_names
    assert "track2.mp3" in discovered_names
    assert "track3.m4a" in discovered_names
    assert "track4.wav" in discovered_names
    assert "album.cue" not in discovered_names
    assert "album.log" not in discovered_names
    assert "cover.jpg" not in discovered_names
    assert len(discovered) == 4


def test_find_cover_art(tmp_path: Path):
    """Тестирование поиска обложки по имени файла."""
    cover_file = tmp_path / "folder.jpg"
    cover_file.write_bytes(b"JPEG_COVER")

    found = find_cover_art(tmp_path, [])
    assert found == cover_file


def test_find_cover_art_none(tmp_path: Path):
    """Тестирование отсутствия обложки."""
    found = find_cover_art(tmp_path, [])
    assert found is None


def test_extract_tags_from_file_mock(tmp_path: Path):
    """Тестирование извлечения тегов mutagen EasyID3."""
    dummy_file = tmp_path / "track.flac"
    dummy_file.write_bytes(b"")

    mock_audio = {
        "title": ["Harder, Better, Faster, Stronger"],
        "artist": ["Daft Punk"],
        "album": ["Discovery"],
        "albumartist": ["Daft Punk"],
        "tracknumber": ["4/14"],
        "date": ["2001"],
        "genre": ["Electronic"],
    }
    info_mock = MagicMock()
    info_mock.length = 224.5

    mock_mutagen = MagicMock()
    mock_mutagen.get = lambda k: mock_audio.get(k)
    mock_mutagen.info = info_mock

    with patch("mutagen.File", return_value=mock_mutagen):
        tags = extract_tags_from_file(dummy_file)
        assert tags["title"] == "Harder, Better, Faster, Stronger"
        assert tags["artist"] == "Daft Punk"
        assert tags["album"] == "Discovery"
        assert tags["track_number"] == 4
        assert tags["year"] == 2001
        assert tags["duration"] == 224


@pytest.mark.asyncio
async def test_torrent_download_success(tmp_path: Path):
    """Тестирование успешного выполнения download_torrent с парсингом прогресса."""
    settings = Settings(
        MUSIC_DIR=tmp_path / "music",
        SECRET_KEY="test_secret_key_long_enough_16",
        ADMIN_PASSWORD_HASH="$2b$12$e867abcdefghijklmnopqrstuvwxyz0123456789",
    )
    svc = TorrentService(settings)

    # Имитируем вывод aria2c в stdout
    lines = [
        b"[#abc123 0B/100MiB(0%) CN:2 DL:0B]\n",
        b"[#abc123 50MiB/100MiB(50%) CN:8 DL:2.5MiB ETA:20s]\n",
        b"[#abc123 100MiB/100MiB(100%) CN:0 DL:0B]\n",
        b"",
    ]

    mock_stdout = MagicMock()
    mock_stdout.readline = AsyncMock(side_effect=lines)

    proc_mock = MagicMock()
    proc_mock.returncode = 0
    proc_mock.stdout = mock_stdout
    proc_mock.wait = AsyncMock(return_value=0)

    progress_reports = []

    def on_prog(percent, speed, peers, eta):
        progress_reports.append((percent, speed, peers, eta))

    with patch("asyncio.create_subprocess_exec", AsyncMock(return_value=proc_mock)):
        await svc.download_torrent(
            magnet_or_torrent_path="magnet:?xt=urn:btih:abcdef",
            output_dir=tmp_path / "download",
            on_progress=on_prog,
        )

    assert len(progress_reports) == 3
    assert progress_reports[0][0] == 0
    assert progress_reports[1][0] == 50
    assert progress_reports[1][1] == "2.5MiB"
    assert progress_reports[1][2] == "8"
    assert progress_reports[2][0] == 100


@pytest.mark.asyncio
async def test_torrent_download_failure(tmp_path: Path):
    """Тестирование обработки ошибки aria2c (ненулевой returncode)."""
    settings = Settings(
        MUSIC_DIR=tmp_path / "music",
        SECRET_KEY="test_secret_key_long_enough_16",
        ADMIN_PASSWORD_HASH="$2b$12$e867abcdefghijklmnopqrstuvwxyz0123456789",
    )
    svc = TorrentService(settings)

    mock_stdout = MagicMock()
    mock_stdout.readline = AsyncMock(return_value=b"")

    proc_mock = MagicMock()
    proc_mock.returncode = 1
    proc_mock.stdout = mock_stdout
    proc_mock.wait = AsyncMock(return_value=1)

    with (
        patch("asyncio.create_subprocess_exec", AsyncMock(return_value=proc_mock)),
        pytest.raises(TorrentError, match="aria2c завершился с кодом 1"),
    ):
        await svc.download_torrent(
            magnet_or_torrent_path="magnet:?xt=urn:btih:abcdef",
            output_dir=tmp_path / "download",
        )


@pytest.mark.asyncio
async def test_torrent_download_size_limit_exceeded(tmp_path: Path):
    """Тестирование отсечения раздачи, размер которой превышает TORRENT_MAX_SIZE_GB."""
    settings = Settings(
        MUSIC_DIR=tmp_path / "music",
        SECRET_KEY="test_secret_key_long_enough_16",
        ADMIN_PASSWORD_HASH="$2b$12$e867abcdefghijklmnopqrstuvwxyz0123456789",
        TORRENT_MAX_SIZE_GB=10,  # Лимит 10 ГБ
    )
    svc = TorrentService(settings)

    # Вывод aria2c сообщает о размере 80 ГБ
    lines = [
        b"[#abc123 100MiB/80.0GiB(1%) CN:5 DL:1.2MiB ETA:10h]\n",
        b"",
    ]

    mock_stdout = MagicMock()
    mock_stdout.readline = AsyncMock(side_effect=lines)

    proc_mock = MagicMock()
    proc_mock.returncode = -15
    proc_mock.stdout = mock_stdout
    proc_mock.wait = AsyncMock(return_value=-15)
    proc_mock.terminate = MagicMock()

    with (
        patch("asyncio.create_subprocess_exec", AsyncMock(return_value=proc_mock)),
        pytest.raises(TorrentError, match="Размер раздачи превышает лимит 10 ГБ"),
    ):
        await svc.download_torrent(
            magnet_or_torrent_path="magnet:?xt=urn:btih:abcdef",
            output_dir=tmp_path / "download",
        )
    proc_mock.terminate.assert_called_once()
