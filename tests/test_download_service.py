"""Тесты сервиса оркестрации загрузок (DownloadService) и конвейера обработки."""

import asyncio
import re
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from mutagen.id3 import ID3

from app.config import Settings
from app.core.exceptions import DownloadError, NotFoundError
from app.core.sandbox import PathSandbox
from app.schemas.download import JobStatus, ResolvedTrack
from app.services.deezer_client import DeezerClient
from app.services.download_service import DownloadService
from app.services.source_resolver import SourceResolver


@pytest.fixture
def download_service(test_settings: Settings, tmp_sandbox: PathSandbox) -> DownloadService:
    """Предоставляет экземпляр DownloadService с тестовыми настройками."""
    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.download_cover_bytes = AsyncMock(return_value=b"fake_cover_data_bytes")

    mock_resolver = MagicMock(spec=SourceResolver)
    mock_resolver.detect_source.return_value = "deezer"

    return DownloadService(
        settings=test_settings,
        tmp_sandbox=tmp_sandbox,
        source_resolver=mock_resolver,
        deezer_client=mock_deezer,
    )


@pytest.mark.asyncio
async def test_create_and_get_job(download_service: DownloadService):
    """Проверка регистрации и получения задачи из памяти."""
    with patch.object(download_service, "_execute_pipeline", new=AsyncMock()):
        job = await download_service.create_job(
            url="https://www.deezer.com/album/302127",
            source="deezer",
        )

        assert job.id is not None
        assert job.status == JobStatus.QUEUED
        assert job.source == "deezer"

        retrieved = download_service.get_job(job.id)
        assert retrieved.id == job.id

        resp = retrieved.to_response()
        assert resp.id == job.id
        assert resp.status == JobStatus.QUEUED


def test_get_nonexistent_job(download_service: DownloadService):
    """Проверка выброса NotFoundError при запросе несуществующей задачи."""
    with pytest.raises(NotFoundError):
        download_service.get_job("nonexistent_id_12345")


@pytest.mark.asyncio
async def test_execute_pipeline_success(
    download_service: DownloadService,
    tmp_sandbox: PathSandbox,
):
    """Проверка успешного прохождения всего пайплайна загрузки и конвертации."""
    sample_track = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="One More Time",
        artist="Daft Punk\\\\Romanthony",
        album="Discovery",
        album_artist="Daft Punk",
        year=2001,
        genre="House",
        duration=320,
        cover_url="https://cover.jpg",
        audio_source_url="https://www.youtube.com/watch?v=sample",
    )

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {
                "album": "Discovery",
                "album_artist": "Daft Punk",
                "year": 2001,
                "genre": "House",
                "cover_url": "https://cover.jpg",
            },
            [sample_track],
        )
    )

    # Фейковый загрузчик сырого аудио (создает файл)
    async def fake_download(source_url, output_template, raw_dir, track_idx):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy_raw_audio")
        return raw_file

    # Фейковый ffmpeg (создает целевой mp3)
    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"dummy_mp3_data")

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
    ):
        job = await download_service.create_job(
            url="https://www.deezer.com/album/302127",
            source="deezer",
        )
        # Дожидаемся завершения фонового конвейера
        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        assert job.progress == 100.0
        assert job.completed_tracks == 1

        # Проверяем получение подготовленных метаданных треков
        tracks_resp = download_service.get_job_tracks(job.id)
        assert tracks_resp.album == "Discovery"
        assert tracks_resp.album_artist == "Daft Punk"
        assert len(tracks_resp.tracks) == 1
        assert tracks_resp.tracks[0].status == "ready"


def test_write_draft_tags(download_service: DownloadService, tmp_path: Path):
    """Проверка корректной записи ID3v2.4 тегов через mutagen."""
    mp3_file = tmp_path / "test_track.mp3"
    mp3_file.write_bytes(b"\xff\xfb\x90\x64" + b"\x00" * 128)

    cover_file = tmp_path / "test_cover.jpg"
    cover_file.write_bytes(b"\xff\xd8\xff\xe0" + b"\x00" * 32)  # dummy jpeg header

    track = ResolvedTrack(
        track_id="10",
        track_number=5,
        title="Harder, Better, Faster, Stronger",
        artist="Daft Punk\\\\Guy-Manuel",
        album="Discovery",
        album_artist="Daft Punk",
        year=2001,
        genre="Electronic\\\\House",
        duration=224,
        audio_source_url="https://yt.com/sample",
    )

    download_service._write_draft_tags(
        mp3_path=mp3_file,
        track=track,
        cover_file=cover_file,
    )

    # Читаем записанные теги обратно через mutagen
    tags = ID3(str(mp3_file))
    assert tags.version == (2, 4, 0)
    assert tags.getall("TIT2")[0].text == ["Harder, Better, Faster, Stronger"]
    # Множественные исполнители в ID3v2.4 хранятся списком
    assert tags.getall("TPE1")[0].text == ["Daft Punk", "Guy-Manuel"]
    assert tags.getall("TALB")[0].text == ["Discovery"]
    assert tags.getall("TPE2")[0].text == ["Daft Punk"]
    assert tags.getall("TRCK")[0].text == ["5"]
    assert str(tags.getall("TDRC")[0].text[0]) == "2001"
    assert tags.getall("TCON")[0].text == ["Electronic", "House"]
    assert len(tags.getall("APIC")) == 1
    assert tags.getall("APIC")[0].type == 3


@pytest.mark.asyncio
async def test_pipeline_error_handling(download_service: DownloadService):
    """Проверка перевода задачи в статус ERROR при сбое разрешения источника."""
    download_service._resolver.resolve = AsyncMock(side_effect=DownloadError("Network error"))

    job = await download_service.create_job(
        url="https://www.deezer.com/album/302127",
        source="deezer",
    )

    for _ in range(50):
        if job.status in (JobStatus.READY, JobStatus.ERROR):
            break
        await asyncio.sleep(0.05)

    assert job.status == JobStatus.ERROR
    assert "Network error" in (job.error or "")

    with pytest.raises(DownloadError) as exc_info:
        download_service.get_job_tracks(job.id)
    assert "Network error" in str(exc_info.value)


@pytest.mark.asyncio
async def test_cleanup_expired_jobs(
    download_service: DownloadService,
    tmp_sandbox: PathSandbox,
):
    """Проверка удаления устаревших задач и каталогов по TTL."""
    now = datetime.now(timezone.utc)
    old_time = now - timedelta(hours=download_service._settings.JOB_TTL_HOURS + 2)

    # 1. Добавляем старую задачу в реестр
    old_job = await download_service.create_job("https://deezer.com/album/1")
    old_job.created_at = old_time

    # Создаем папку старой задачи
    old_dir = tmp_sandbox.resolve(old_job.id)
    old_dir.mkdir(parents=True, exist_ok=True)
    (old_dir / "sample.txt").write_text("old")

    # 2. Добавляем свежую задачу
    fresh_job = await download_service.create_job("https://deezer.com/album/2")
    fresh_dir = tmp_sandbox.resolve(fresh_job.id)
    fresh_dir.mkdir(parents=True, exist_ok=True)

    # Запуск сборщика мусора
    removed = download_service.cleanup_expired_jobs()
    assert removed >= 1

    # Старая задача удалена
    assert old_job.id not in download_service._jobs
    assert not old_dir.exists()

    # Свежая задача осталась
    assert fresh_job.id in download_service._jobs
    assert fresh_dir.exists()


@pytest.mark.asyncio
async def test_download_raw_stream_with_cookies(
    download_service: DownloadService,
    tmp_path: Path,
):
    """Проверка приоритета гостевого режима и отката на --cookies при ошибке Age Gate."""
    cookies_file = tmp_path / "cookies.txt"
    cookies_file.write_text("# Netscape HTTP Cookie File")

    download_service._settings.YTDLP_COOKIES_PATH = cookies_file

    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)
    output_file = raw_dir / "01.opus"
    output_file.write_bytes(b"dummy_raw_stream_bytes")

    # Создаем фиктивный частичный файл, который должен быть очищен при откате
    part_file = raw_dir / "01.opus.part"
    part_file.write_bytes(b"partial_bytes")

    mock_proc_guest = AsyncMock()
    mock_proc_guest.returncode = 1
    mock_proc_guest.communicate.return_value = (b"", b"ERROR: Sign in to confirm your age")
    mock_proc_guest.stderr.readline = AsyncMock(side_effect=[b"ERROR: Sign in to confirm your age\n", b""])
    mock_proc_guest.stdout.readline = AsyncMock(return_value=b"")

    mock_proc_cookies = AsyncMock()
    mock_proc_cookies.returncode = 0
    mock_proc_cookies.communicate.return_value = (b"", b"")
    mock_proc_cookies.stderr.readline = AsyncMock(return_value=b"")
    mock_proc_cookies.stdout.readline = AsyncMock(return_value=b"")

    with patch("asyncio.create_subprocess_exec", side_effect=[mock_proc_guest, mock_proc_cookies]) as mock_exec:
        result_path = await download_service._download_raw_stream(
            source_url="https://www.youtube.com/watch?v=eV-DHvPP6d8",
            output_template=raw_dir / "%(title)s.%(ext)s",
            raw_dir=raw_dir,
            track_idx=1,
        )

        assert result_path == output_file
        assert mock_exec.call_count == 2

        # 1-й вызов: быстрый гостевой режим без cookies
        call_guest = mock_exec.call_args_list[0][0]
        assert "--cookies" not in call_guest
        assert "youtube:player_client=ios,visionos" in call_guest

        # 2-й вызов: повтор с cookies при Age Gate
        call_cookies = mock_exec.call_args_list[1][0]
        assert "--cookies" in call_cookies
        cookies_idx = call_cookies.index("--cookies")
        assert call_cookies[cookies_idx + 1] == str(cookies_file.resolve())
        assert call_cookies[-1] == "https://www.youtube.com/watch?v=eV-DHvPP6d8"

        # Проверяем, что временный part-файл был удален
        assert not part_file.exists()


@pytest.mark.asyncio
async def test_download_raw_stream_prioritizes_guest_mode(
    download_service: DownloadService,
    tmp_path: Path,
):
    """Проверка, что при успешном гостевом скачивании cookies вообще не используются."""
    cookies_file = tmp_path / "cookies.txt"
    cookies_file.write_text("# Netscape HTTP Cookie File")
    download_service._settings.YTDLP_COOKIES_PATH = cookies_file

    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)
    output_file = raw_dir / "01.opus"
    output_file.write_bytes(b"dummy_raw_stream_bytes")

    mock_proc = AsyncMock()
    mock_proc.returncode = 0
    mock_proc.communicate.return_value = (b"", b"")
    mock_proc.stderr.readline = AsyncMock(return_value=b"")
    mock_proc.stdout.readline = AsyncMock(return_value=b"")

    with patch("asyncio.create_subprocess_exec", return_value=mock_proc) as mock_exec:
        result_path = await download_service._download_raw_stream(
            source_url="https://www.youtube.com/watch?v=good_video",
            output_template=raw_dir / "%(title)s.%(ext)s",
            raw_dir=raw_dir,
            track_idx=1,
        )

        assert result_path == output_file
        assert mock_exec.call_count == 1
        call_args = mock_exec.call_args[0]
        assert "--cookies" not in call_args
        assert "youtube:player_client=ios,visionos" in call_args


@pytest.mark.asyncio
async def test_download_raw_stream_non_age_failure_fast_fail(
    download_service: DownloadService,
    tmp_path: Path,
):
    """Проверка, что при ошибке, не связанной с возрастом, повтора с cookies не происходит."""
    cookies_file = tmp_path / "cookies.txt"
    cookies_file.write_text("# Netscape HTTP Cookie File")
    download_service._settings.YTDLP_COOKIES_PATH = cookies_file

    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)

    mock_proc = AsyncMock()
    mock_proc.returncode = 1
    mock_proc.communicate.return_value = (b"", b"ERROR: Video unavailable (404 Not Found)")
    mock_proc.stderr.readline = AsyncMock(side_effect=[b"ERROR: Video unavailable (404 Not Found)\n", b""])
    mock_proc.stdout.readline = AsyncMock(return_value=b"")

    with patch("asyncio.create_subprocess_exec", return_value=mock_proc) as mock_exec:
        with pytest.raises(DownloadError, match="Video unavailable"):
            await download_service._download_raw_stream(
                source_url="https://www.youtube.com/watch?v=deleted_video",
                output_template=raw_dir / "%(title)s.%(ext)s",
                raw_dir=raw_dir,
                track_idx=1,
            )

        # Вызов должен быть строго один (без бесполезного повтора с cookies)
        assert mock_exec.call_count == 1


@pytest.mark.asyncio
async def test_download_raw_stream_age_gate_without_cookies_raises(
    download_service: DownloadService,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
):
    """Проверка, что при Age Gate и отсутствии cookies сразу выбрасывается DownloadError."""
    empty_dir = tmp_path / "empty_dir"
    empty_dir.mkdir(parents=True, exist_ok=True)
    monkeypatch.setattr("app.config.PROJECT_ROOT", empty_dir)
    monkeypatch.setattr(Path, "cwd", lambda: empty_dir)
    download_service._settings.YTDLP_COOKIES_PATH = None

    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)

    mock_proc = AsyncMock()
    mock_proc.returncode = 1
    mock_proc.communicate.return_value = (b"", b"ERROR: Sign in to confirm your age")
    mock_proc.stderr.readline = AsyncMock(side_effect=[b"ERROR: Sign in to confirm your age\n", b""])
    mock_proc.stdout.readline = AsyncMock(return_value=b"")

    with patch("asyncio.create_subprocess_exec", return_value=mock_proc) as mock_exec:
        with pytest.raises(DownloadError, match="confirm your age"):
            await download_service._download_raw_stream(
                source_url="https://www.youtube.com/watch?v=restricted_video",
                output_template=raw_dir / "%(title)s.%(ext)s",
                raw_dir=raw_dir,
                track_idx=1,
            )

        assert mock_exec.call_count == 1


@pytest.mark.asyncio
async def test_download_raw_stream_without_cookies(
    download_service: DownloadService,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
):
    """Проверка отсутствия --cookies в вызове yt-dlp, когда cookies_file отсутствует."""
    empty_dir = tmp_path / "empty_dir"
    empty_dir.mkdir(parents=True, exist_ok=True)
    monkeypatch.setattr("app.config.PROJECT_ROOT", empty_dir)
    monkeypatch.setattr(Path, "cwd", lambda: empty_dir)
    download_service._settings.YTDLP_COOKIES_PATH = None

    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)
    output_file = raw_dir / "02.webm"
    output_file.write_bytes(b"dummy_raw_stream_bytes")

    mock_proc = AsyncMock()
    mock_proc.returncode = 0
    mock_proc.communicate.return_value = (b"", b"")

    with patch("asyncio.create_subprocess_exec", return_value=mock_proc) as mock_exec:
        result_path = await download_service._download_raw_stream(
            source_url="https://www.youtube.com/watch?v=sample123",
            output_template=raw_dir / "%(title)s.%(ext)s",
            raw_dir=raw_dir,
            track_idx=2,
        )

        assert result_path == output_file
        assert mock_exec.called
        call_args = mock_exec.call_args[0]
        assert "--cookies" not in call_args
        assert call_args[-1] == "https://www.youtube.com/watch?v=sample123"


def test_progress_regex_parsing():
    """Проверка парсинга процентов загрузки yt-dlp через регулярное выражение."""
    pattern = DownloadService.PROGRESS_REGEX

    # Bare percentages (templated output from real yt-dlp)
    m_bare1 = pattern.search("  14.7%")
    assert m_bare1 is not None and float(m_bare1.group(1)) == 14.7

    m_bare2 = pattern.search(" 0.0%")
    assert m_bare2 is not None and float(m_bare2.group(1)) == 0.0

    m_bare3 = pattern.search("100.0%")
    assert m_bare3 is not None and float(m_bare3.group(1)) == 100.0

    m_bare4 = pattern.search("100%")
    assert m_bare4 is not None and float(m_bare4.group(1)) == 100.0

    # Templated output with prefix
    m1 = pattern.search("download: 45.2%")
    assert m1 is not None and float(m1.group(1)) == 45.2

    m2 = pattern.search("download:100.0%")
    assert m2 is not None and float(m2.group(1)) == 100.0

    m3 = pattern.search("download: 0.0%")
    assert m3 is not None and float(m3.group(1)) == 0.0

    # Fallback standard yt-dlp format
    m4 = pattern.search("[download]  50.0% of 12.34MiB at 1.23MiB/s ETA 00:05")
    assert m4 is not None and float(m4.group(1)) == 50.0

    m5 = pattern.search("[download] 100% of 12.34MiB in 00:10")
    assert m5 is not None and float(m5.group(1)) == 100.0

    m6 = pattern.search("[download]  50.0% of 10MB")
    assert m6 is not None and float(m6.group(1)) == 50.0

    # ANSI escape code stripping
    ansi_line = "\x1b[0;94m 14.7%\x1b[0m"
    clean_line = re.sub(r"\x1b\[[0-9;]*[a-zA-Z]", "", ansi_line)
    m_ansi = pattern.search(clean_line)
    assert m_ansi is not None and float(m_ansi.group(1)) == 14.7

    # Non-progress lines
    assert pattern.search("[youtube] Extracting URL") is None
    assert pattern.search("[info] Writing output to...") is None
    assert pattern.search("[download] Destination: 01.webm") is None
    assert pattern.search("  N/A%") is None
    assert pattern.search("download: N/A%") is None


@pytest.mark.parametrize(
    ("line", "expected_percent"),
    [
        ("  14.7%", 14.7),
        ("  0.0%", 0.0),
        (" 99.9%", 99.9),
        ("100.0%", 100.0),
        ("100%", 100.0),
        ("  0%", 0.0),
        ("download: 45.2%", 45.2),
        ("download:  14.7%", 14.7),
        ("download:100.0%", 100.0),
        ("download: 0.0%", 0.0),
        ("[download]  50.0% of 12.34MiB at 1.23MiB/s ETA 00:05", 50.0),
        ("[download]   0.0% of ~5.00MiB at 250.00KiB/s ETA 00:10", 0.0),
        ("[download] 100% of 12.34MiB in 00:10", 100.0),
        ("[download]  50.0% of 10MB", 50.0),
        ("\x1b[0;94m 14.7%\x1b[0m", 14.7),
        ("download:\x1b[0;32m 55.5%\x1b[0m", 55.5),
    ],
)
def test_progress_regex_parsing_robust(line: str, expected_percent: float):
    """Параметризованная проверка парсинга процентов с учетом префиксов и ANSI."""
    ansi_clean = re.sub(r"\x1b\[[0-9;]*[a-zA-Z]", "", line)
    match = DownloadService.PROGRESS_REGEX.search(ansi_clean)
    assert match is not None, f"Failed to match line: {line!r}"
    assert float(match.group(1)) == expected_percent


@pytest.mark.asyncio
async def test_download_raw_stream_streaming_progress(
    download_service: DownloadService,
    tmp_path: Path,
):
    """Проверка потокового парсинга вывода yt-dlp и вызова on_progress с bare и ANSI строками."""
    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)
    output_file = raw_dir / "01.opus"
    output_file.write_bytes(b"dummy_stream")

    stdout_reader = asyncio.StreamReader()
    stdout_reader.feed_data(
        b"\x1b[0;94m  0.0%\x1b[0m\n"
        b"  14.7%\n"
        b"download: 50.0%\n"
        b"[download]  80.0% of 10MB\n"
        b"100.0%\n"
    )
    stdout_reader.feed_eof()

    stderr_reader = asyncio.StreamReader()
    stderr_reader.feed_eof()

    mock_proc = AsyncMock()
    mock_proc.returncode = 0
    mock_proc.stdout = stdout_reader
    mock_proc.stderr = stderr_reader
    mock_proc.wait = AsyncMock(return_value=0)

    progress_values: list[float] = []

    def on_prog(p: float) -> None:
        progress_values.append(p)

    with patch("asyncio.create_subprocess_exec", return_value=mock_proc) as mock_exec:
        result_path = await download_service._download_raw_stream(
            source_url="https://www.youtube.com/watch?v=streaming123",
            output_template=raw_dir / "%(title)s.%(ext)s",
            raw_dir=raw_dir,
            track_idx=1,
            on_progress=on_prog,
        )

        assert result_path == output_file
        assert progress_values == [0.0, 14.7, 50.0, 80.0, 100.0]

        # Verify yt-dlp arguments include streaming flags and --no-colors
        call_args = mock_exec.call_args[0]
        assert "--newline" in call_args
        assert "--no-colors" in call_args
        assert "--progress" in call_args
        assert "--progress-template" in call_args
        assert "--progress-delta" in call_args
        assert "--ignore-config" in call_args


@pytest.mark.asyncio
async def test_multi_track_fluctuating_progress_monotonicity(
    download_service: DownloadService,
    tmp_path: Path,
):
    """Проверка строгой монотонности job.progress при скачках процентов назад (re-transmissions)."""
    track_1 = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="Fluctuating Track One",
        artist="Artist",
        duration=180,
        audio_source_url="https://youtube.com/watch?v=fluct1",
    )
    track_2 = ResolvedTrack(
        track_id="2",
        track_number=2,
        title="Fluctuating Track Two",
        artist="Artist",
        duration=210,
        audio_source_url="https://youtube.com/watch?v=fluct2",
    )

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {"album": "Monotonic Album", "album_artist": "Artist"},
            [track_1, track_2],
        )
    )

    recorded_progress: list[float] = []
    recorded_steps: list[str] = []

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy_data")
        job = next(iter(download_service._jobs.values()))

        if on_progress:
            # Simulate normal progress, then regression due to packet retry, then advance
            samples = [10.0, 45.0, 20.0, 60.0, 35.0, 80.0, 100.0]
            for val in samples:
                on_progress(val)
                recorded_progress.append(job.progress)
                recorded_steps.append(job.current_step)
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"mp3_data")
        job = next(iter(download_service._jobs.values()))
        recorded_progress.append(job.progress)
        recorded_steps.append(job.current_step)

    def fake_tags(mp3_path, track, cover_file):
        job = next(iter(download_service._jobs.values()))
        recorded_progress.append(job.progress)
        recorded_steps.append(job.current_step)

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(download_service, "_write_draft_tags", side_effect=fake_tags),
    ):
        job = await download_service.create_job(
            url="https://www.deezer.com/album/monotonic_test",
            source="deezer",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        assert job.progress == 100.0

        # Verify strictly monotonic: no sample is strictly less than previous
        assert len(recorded_progress) > 10
        for i in range(1, len(recorded_progress)):
            assert recorded_progress[i] >= recorded_progress[i - 1], (
                f"Progress decreased from {recorded_progress[i - 1]} to {recorded_progress[i]} at index {i}"
            )

        # Verify job.current_step formats track number, total, and title
        track_1_steps = [s for s in recorded_steps if "Fluctuating Track One" in s]
        assert len(track_1_steps) >= 3
        # Check download step format
        assert any("Загрузка трека 1/2: Fluctuating Track One" in s for s in track_1_steps)
        # Check conversion step format
        assert any("Конвертация трека 1/2: Fluctuating Track One" in s for s in track_1_steps)
        # Check tagging step format
        assert any("Запись ID3v2.4 тегов 1/2: Fluctuating Track One" in s for s in track_1_steps)



@pytest.mark.asyncio
async def test_execute_pipeline_monotonic_stage_budgeting(
    download_service: DownloadService,
):
    """Проверка плавного и монотонного роста прогресса по этапам (0-5-10-100%)."""
    track_1 = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="Track One",
        artist="Artist",
        album="Album",
        album_artist="Artist",
        duration=180,
        audio_source_url="https://youtube.com/watch?v=track1",
    )
    track_2 = ResolvedTrack(
        track_id="2",
        track_number=2,
        title="Track Two",
        artist="Artist",
        album="Album",
        album_artist="Artist",
        duration=200,
        audio_source_url="https://youtube.com/watch?v=track2",
    )

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {
                "album": "Album",
                "album_artist": "Artist",
                "year": 2024,
                "genre": "Pop",
                "cover_url": "https://cover.jpg",
            },
            [track_1, track_2],
        )
    )

    progress_samples: list[float] = []

    def get_current_progress() -> float:
        jobs = list(download_service._jobs.values())
        return jobs[0].progress if jobs else 0.0

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy_raw_audio")
        if on_progress:
            on_progress(25.0)
            progress_samples.append(get_current_progress())
            on_progress(75.0)
            progress_samples.append(get_current_progress())
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"dummy_mp3_data")
        progress_samples.append(get_current_progress())

    def fake_tags(mp3_path, track, cover_file):
        progress_samples.append(get_current_progress())

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(download_service, "_write_draft_tags", side_effect=fake_tags),
    ):
        job = await download_service.create_job(
            url="https://www.deezer.com/album/999",
            source="deezer",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        assert job.progress == 100.0
        assert job.completed_tracks == 2

        # Прогресс должен строго монотонно возрастать
        assert len(progress_samples) >= 6
        for i in range(1, len(progress_samples)):
            assert progress_samples[i] >= progress_samples[i - 1], (
                f"Progress regressed at index {i}: {progress_samples[i]} < {progress_samples[i-1]}"
            )
        # Начальный прогресс треков начинается от >= 10.0%
        assert progress_samples[0] >= 10.0
        assert progress_samples[-1] <= 100.0


@pytest.mark.asyncio
async def test_execute_pipeline_backward_compatible_4arg_mock(
    download_service: DownloadService,
):
    """Проверка обратной совместимости с тестовыми моками _download_raw_stream с 4 аргументами."""
    track = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="Single Track",
        artist="Artist",
        album="Album",
        album_artist="Artist",
        duration=180,
        audio_source_url="https://youtube.com/watch?v=single",
    )

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {"album": "Album", "album_artist": "Artist"},
            [track],
        )
    )

    # 4-arg signature (raises TypeError if called with on_progress keyword arg)
    async def fake_download_4arg(source_url, output_template, raw_dir, track_idx):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"mp3")

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download_4arg),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(download_service, "_write_draft_tags", return_value=None),
    ):
        job = await download_service.create_job(
            url="https://www.deezer.com/album/111",
            source="deezer",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        assert job.progress == 100.0


@pytest.mark.asyncio
async def test_execute_pipeline_skips_errored_tracks(
    download_service: DownloadService,
):
    """Проверка пропуска треков со статусом error в _execute_pipeline."""
    track1 = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="Good Track",
        artist="Artist",
        album="Album",
        album_artist="Artist",
        duration=180,
        audio_source_url="https://youtube.com/watch?v=good",
        status="pending",
    )
    track2 = ResolvedTrack(
        track_id="2",
        track_number=2,
        title="Bad Track",
        artist="Artist",
        album="Album",
        album_artist="Artist",
        duration=180,
        audio_source_url="",
        status="error",
        error="No candidate found",
    )

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {"album": "Album", "album_artist": "Artist"},
            [track1, track2],
        )
    )

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"mp3")

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download) as mock_dl,
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(download_service, "_write_draft_tags", return_value=None),
    ):
        job = await download_service.create_job(
            url="https://www.deezer.com/album/222",
            source="deezer",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        assert job.progress == 100.0
        assert job.completed_tracks == 1
        assert mock_dl.call_count == 1
        assert track1.status == "ready"
        assert track2.status == "error"


def test_is_drm_protected_error():
    """Проверка распознавания ошибок DRM защиты (SoundCloud, etc.)."""
    from app.services.download_service import is_drm_protected_error

    assert is_drm_protected_error("ERROR: [soundcloud] 2385324699: This video is DRM protected")
    assert is_drm_protected_error("This video is DRM protected")
    assert is_drm_protected_error("This audio is DRM protected")
    assert is_drm_protected_error("The stream is drm-protected")
    assert is_drm_protected_error("Track has DRM enabled")

    assert not is_drm_protected_error("")
    assert not is_drm_protected_error("Sign in to confirm your age")
    assert not is_drm_protected_error("HTTP Error 404: Not Found")
    assert not is_drm_protected_error("Network connection reset")


@pytest.mark.asyncio
async def test_execute_pipeline_skips_drm_protected_track_in_playlist(
    download_service: DownloadService,
):
    """Проверка, что DRM-защищенный трек в плейлисте пропускается, а остальные треки скачиваются."""
    track1 = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="Valid Track 1",
        artist="Artist",
        album="Playlist",
        album_artist="Artist",
        duration=150,
        audio_source_url="https://soundcloud.com/artist/track1",
        status="pending",
    )
    track2 = ResolvedTrack(
        track_id="2",
        track_number=2,
        title="DRM Protected Track 2",
        artist="Artist",
        album="Playlist",
        album_artist="Artist",
        duration=180,
        audio_source_url="https://soundcloud.com/artist/track2-drm",
        status="pending",
    )
    track3 = ResolvedTrack(
        track_id="3",
        track_number=3,
        title="Valid Track 3",
        artist="Artist",
        album="Playlist",
        album_artist="Artist",
        duration=200,
        audio_source_url="https://soundcloud.com/artist/track3",
        status="pending",
    )

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {"album": "Playlist", "album_artist": "Artist"},
            [track1, track2, track3],
        )
    )

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        if "track2-drm" in source_url:
            raise DownloadError(
                "Ошибка yt-dlp при скачивании аудио: ERROR: [soundcloud] 2385324699: This video is DRM protected"
            )
        raw_file = raw_dir / f"{track_idx:02d}.mp3"
        raw_file.write_bytes(b"dummy_audio")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"dummy_mp3")

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(download_service, "_write_draft_tags", return_value=None),
    ):
        job = await download_service.create_job(
            url="https://soundcloud.com/artist/sets/my-playlist",
            source="soundcloud",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        # Плейлист завершается в READY, а не в ERROR
        assert job.status == JobStatus.READY
        assert job.progress == 100.0
        assert job.completed_tracks == 2
        assert "1 пропущено" in job.current_step

        # Статусы индивидуальных треков
        assert track1.status == "ready"
        assert track2.status == "error"
        assert "DRM protected" in (track2.error or "")
        assert track3.status == "ready"


@pytest.mark.asyncio
async def test_execute_pipeline_single_drm_track_fails(
    download_service: DownloadService,
):
    """Проверка, что одиночный DRM-защищенный трек (не плейлист) переводит задачу в ERROR."""
    single_track = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="Solo DRM Track",
        artist="Artist",
        album="Album",
        album_artist="Artist",
        duration=180,
        audio_source_url="https://soundcloud.com/artist/solo-drm",
        status="pending",
    )

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {"album": "Album", "album_artist": "Artist"},
            [single_track],
        )
    )

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raise DownloadError(
            "Ошибка yt-dlp при скачивании аудио: ERROR: [soundcloud] 2385324699: This video is DRM protected"
        )

    with patch.object(download_service, "_download_raw_stream", side_effect=fake_download):
        job = await download_service.create_job(
            url="https://soundcloud.com/artist/solo-drm",
            source="soundcloud",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.ERROR
        assert "DRM protected" in (job.error or "")
        assert single_track.status == "error"


@pytest.mark.asyncio
async def test_execute_pipeline_soundcloud_playlist_artwork_and_filenames(
    download_service: DownloadService,
    tmp_sandbox: PathSandbox,
):
    """R1, R2, R3: Проверка скачивания SoundCloud плейлиста, формирования имен файлов и встраивания APIC обложек."""
    track1 = ResolvedTrack(
        track_id="101",
        track_number=1,
        title="Midnight City",
        artist="M83",
        album="Hurry Up, We're Dreaming",
        album_artist="M83",
        duration=243,
        cover_url="https://i1.sndcdn.com/artworks-m83-t500x500.jpg",
        audio_source_url="https://soundcloud.com/m83/midnight-city",
        status="pending",
    )
    track2 = ResolvedTrack(
        track_id="102",
        track_number=2,
        title="Reunion",
        artist="M83",
        album="Hurry Up, We're Dreaming",
        album_artist="M83",
        duration=235,
        cover_url="https://i1.sndcdn.com/artworks-reunion-t500x500.jpg",
        audio_source_url="https://soundcloud.com/m83/reunion",
        status="pending",
    )

    playlist_cover_url = "https://i1.sndcdn.com/artworks-playlist-t500x500.jpg"

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {
                "album": "Hurry Up, We're Dreaming",
                "album_artist": "M83",
                "cover_url": playlist_cover_url,
            },
            [track1, track2],
        )
    )

    async def fake_download_cover_bytes(url: str) -> bytes:
        if "playlist" in url:
            return b"PLAYLIST_COVER_BYTES"
        if "m83" in url:
            return b"M83_TRACK1_COVER_BYTES"
        if "reunion" in url:
            return b"REUNION_TRACK2_COVER_BYTES"
        return b"GENERIC_COVER"

    download_service._deezer.download_cover_bytes = AsyncMock(side_effect=fake_download_cover_bytes)

    async def fake_download_raw(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy_raw_stream")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        # Пишем валидный заголовок MP3 фрейма для mutagen ID3
        output_mp3.write_bytes(b"\xff\xfb\x90\x64" + b"\x00" * 256)

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download_raw),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
    ):
        job = await download_service.create_job(
            url="https://soundcloud.com/m83/sets/hurry-up",
            source="soundcloud",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        assert job.progress == 100.0
        assert job.completed_tracks == 2

        # 1. Проверка сохранения обложки задачи (job cover path)
        job_cover = tmp_sandbox.resolve(f"{job.id}/cover.jpg")
        assert job_cover.exists()
        assert job_cover.read_bytes() == b"PLAYLIST_COVER_BYTES"
        assert job.cover_relative_path == f"{job.id}/cover.jpg"

        # 2. Проверка имен файлов: {track_number:02d}. {title}.mp3
        file1 = tmp_sandbox.resolve(f"{job.id}/converted/01. Midnight City.mp3")
        file2 = tmp_sandbox.resolve(f"{job.id}/converted/02. Reunion.mp3")
        assert file1.exists()
        assert file2.exists()

        # 3. Проверка тегов ID3v2.4 и встроенных APIC обложек через mutagen
        tags1 = ID3(str(file1))
        assert tags1.version == (2, 4, 0)
        assert tags1.getall("TIT2")[0].text == ["Midnight City"]
        assert tags1.getall("TPE1")[0].text == ["M83"]
        assert tags1.getall("TALB")[0].text == ["Hurry Up, We're Dreaming"]
        assert tags1.getall("TRCK")[0].text == ["1"]
        apic1 = tags1.getall("APIC")
        assert len(apic1) == 1
        assert apic1[0].type == 3
        assert apic1[0].data == b"M83_TRACK1_COVER_BYTES"

        tags2 = ID3(str(file2))
        assert tags2.version == (2, 4, 0)
        assert tags2.getall("TIT2")[0].text == ["Reunion"]
        assert tags2.getall("TPE1")[0].text == ["M83"]
        assert tags2.getall("TALB")[0].text == ["Hurry Up, We're Dreaming"]
        assert tags2.getall("TRCK")[0].text == ["2"]
        apic2 = tags2.getall("APIC")
        assert len(apic2) == 1
        assert apic2[0].type == 3
        assert apic2[0].data == b"REUNION_TRACK2_COVER_BYTES"


@pytest.mark.asyncio
async def test_execute_pipeline_soundcloud_playlist_artwork_fallback_to_playlist(
    download_service: DownloadService,
    tmp_sandbox: PathSandbox,
):
    """R2: Если у трека 2 нет индивидуальной обложки (она совпадает с плейлистом), вшивается обложка плейлиста."""
    playlist_cover_url = "https://i1.sndcdn.com/artworks-pl-t500x500.jpg"
    track1 = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="Track With Own Art",
        artist="Artist",
        cover_url="https://i1.sndcdn.com/artworks-track1-t500x500.jpg",
        audio_source_url="https://soundcloud.com/artist/track1",
    )
    track2 = ResolvedTrack(
        track_id="2",
        track_number=2,
        title="Track With Playlist Art Fallback",
        artist="Artist",
        cover_url=playlist_cover_url,  # совпадает с обложкой плейлиста (fallback)
        audio_source_url="https://soundcloud.com/artist/track2",
    )

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {"album": "Set", "album_artist": "Artist", "cover_url": playlist_cover_url},
            [track1, track2],
        )
    )

    async def fake_covers(url: str) -> bytes:
        if "track1" in url:
            return b"TRACK1_ART"
        return b"PLAYLIST_ART"

    download_service._deezer.download_cover_bytes = AsyncMock(side_effect=fake_covers)

    async def fake_download_raw(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"\xff\xfb\x90\x64" + b"\x00" * 128)

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download_raw),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
    ):
        job = await download_service.create_job(
            url="https://soundcloud.com/artist/sets/set",
            source="soundcloud",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        file2 = tmp_sandbox.resolve(f"{job.id}/converted/02. Track With Playlist Art Fallback.mp3")
        assert file2.exists()
        tags2 = ID3(str(file2))
        apic2 = tags2.getall("APIC")
        assert len(apic2) == 1
        assert apic2[0].data == b"PLAYLIST_ART"


@pytest.mark.asyncio
async def test_execute_pipeline_soundcloud_playlist_job_cover_fallback_to_first_track(
    download_service: DownloadService,
    tmp_sandbox: PathSandbox,
):
    """R2: Если у самого плейлиста cover_url отсутствует, обложка первого трека сохраняется как job cover."""
    track1 = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="Track Uno",
        artist="Artist",
        cover_url="https://i1.sndcdn.com/artworks-uno-t500x500.jpg",
        audio_source_url="https://soundcloud.com/artist/uno",
    )

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {"album": "No Cover Set", "album_artist": "Artist", "cover_url": None},
            [track1],
        )
    )

    download_service._deezer.download_cover_bytes = AsyncMock(return_value=b"UNO_COVER_BYTES")

    async def fake_download_raw(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"\xff\xfb\x90\x64" + b"\x00" * 128)

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download_raw),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
    ):
        job = await download_service.create_job(
            url="https://soundcloud.com/artist/sets/no-cover-set",
            source="soundcloud",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        job_cover = tmp_sandbox.resolve(f"{job.id}/cover.jpg")
        assert job_cover.exists()
        assert job_cover.read_bytes() == b"UNO_COVER_BYTES"
        assert job.cover_relative_path == f"{job.id}/cover.jpg"


@pytest.mark.asyncio
async def test_execute_pipeline_non_latin_and_adversarial_filenames_and_tags(
    download_service: DownloadService,
    tmp_sandbox: PathSandbox,
):
    """R1, R2, R3: Проверка нелатинских символов (японский, кириллица) и спецсимволов/инъекций в именах файлов и тегах."""
    track1 = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="夜に駆ける: Racing into the Night [Official Audio] ? <HD>",
        artist="YOASOBI \\\\ Ayase",
        album="THE BOOK",
        album_artist="YOASOBI",
        duration=261,
        cover_url="https://i1.sndcdn.com/artworks-yoasobi-t500x500.jpg",
        audio_source_url="https://soundcloud.com/yoasobi/racing",
        status="pending",
    )
    track2 = ResolvedTrack(
        track_id="2",
        track_number=2,
        title='Группа Крови: *1988* | "Кино" / Переиздание %00 %2f \n',
        artist="Кино\x00Виктор Цой",
        album="Группа крови",
        album_artist="Кино",
        duration=285,
        cover_url="https://i1.sndcdn.com/artworks-kino-t500x500.jpg",
        audio_source_url="https://soundcloud.com/kino/gruppa-krovi",
        status="pending",
    )

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {"album": "International Mix", "album_artist": "Various", "cover_url": None},
            [track1, track2],
        )
    )

    download_service._deezer.download_cover_bytes = AsyncMock(
        side_effect=lambda url: b"COVER_DATA_" + url.encode()
    )

    async def fake_download_raw(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy_stream")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"\xff\xfb\x90\x64" + b"\x00" * 128)

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download_raw),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
    ):
        job = await download_service.create_job(
            url="https://soundcloud.com/curator/sets/international",
            source="soundcloud",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        assert job.completed_tracks == 2

        # 1. Проверяем корректность санитизированных имен файлов в песочнице
        converted_files = sorted((tmp_sandbox.root / job.id / "converted").glob("*.mp3"))
        assert len(converted_files) == 2

        # Японский трек: спецсимволы (: ? < >) заменены на _, японские иероглифы сохранены
        file1 = converted_files[0]
        assert file1.name == "01. 夜に駆ける_ Racing into the Night [Official Audio] _ _HD_.mp3"

        # Кириллический трек: спецсимволы (* | " / % \n) заменены на _, кириллица сохранена
        file2 = converted_files[1]
        assert file2.name == "02. Группа Крови_ _1988_ _ _Кино_ _ Переиздание _00 _2f.mp3"

        # 2. Проверяем теги ID3v2.4
        tags1 = ID3(str(file1))
        assert tags1.version == (2, 4, 0)
        # В тегах сохраняются оригинальные заголовки
        assert tags1.getall("TIT2")[0].text == ["夜に駆ける: Racing into the Night [Official Audio] ? <HD>"]
        # Множественные исполнители (\\\\) сохранены в виде раздельных элементов
        assert tags1.getall("TPE1")[0].text == ["YOASOBI", "Ayase"]
        assert len(tags1.getall("APIC")) == 1

        tags2 = ID3(str(file2))
        assert tags2.version == (2, 4, 0)
        assert tags2.getall("TIT2")[0].text == ['Группа Крови: *1988* | "Кино" / Переиздание %00 %2f \n']
        # Множественные исполнители (\\x00) сохранены в виде раздельных элементов
        assert tags2.getall("TPE1")[0].text == ["Кино", "Виктор Цой"]
        assert len(tags2.getall("APIC")) == 1


@pytest.mark.asyncio
async def test_execute_pipeline_playlist_cover_network_failure_fallback_to_track_cover(
    download_service: DownloadService,
    tmp_sandbox: PathSandbox,
):
    """R2: При сбое скачивания обложки самого плейлиста обложка трека успешно используется и как job cover, и в APIC."""
    track1 = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="Track 1",
        artist="Artist 1",
        cover_url="https://i1.sndcdn.com/artworks-track1-t500x500.jpg",
        audio_source_url="https://soundcloud.com/artist/track1",
    )

    download_service._resolver.resolve = AsyncMock(
        return_value=(
            {
                "album": "Set",
                "album_artist": "Artist",
                # Ссылка на обложку плейлиста есть, но ее скачивание завершится ошибкой
                "cover_url": "https://i1.sndcdn.com/artworks-failing-playlist.jpg",
            },
            [track1],
        )
    )

    async def fake_cover_download(url: str) -> bytes:
        if "failing-playlist" in url:
            raise RuntimeError("HTTP 500 Playlist Cover Unavailable")
        return b"RECOVERED_TRACK_COVER_BYTES"

    download_service._deezer.download_cover_bytes = AsyncMock(side_effect=fake_cover_download)

    async def fake_download_raw(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"\xff\xfb\x90\x64" + b"\x00" * 128)

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download_raw),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
    ):
        job = await download_service.create_job(
            url="https://soundcloud.com/artist/sets/resilience",
            source="soundcloud",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        assert job.completed_tracks == 1

        # Обложка задачи должна быть сохранена из обложки трека
        job_cover = tmp_sandbox.resolve(f"{job.id}/cover.jpg")
        assert job_cover.exists()
        assert job_cover.read_bytes() == b"RECOVERED_TRACK_COVER_BYTES"

        # Обложка трека должна быть вшита в ID3 APIC
        mp3_file = tmp_sandbox.resolve(f"{job.id}/converted/01. Track 1.mp3")
        tags = ID3(str(mp3_file))
        assert len(tags.getall("APIC")) == 1
        assert tags.getall("APIC")[0].data == b"RECOVERED_TRACK_COVER_BYTES"


@pytest.mark.asyncio
async def test_download_pipeline_duplicate_filename_disambiguation(tmp_sandbox):
    """R1, R3: Защита от перезаписи файлов при совпадении имен треков внутри одной задачи."""
    from app.config import Settings
    from app.services.download_service import DownloadService

    settings = Settings(
        SECRET_KEY="test_secret_key_fixed_length_32_bytes!!",
        ADMIN_PASSWORD_HASH="$2b$12$e8Y5t1hWzC181zK0m7oKeej1eM0.7tH2l4aG5p8fW9xK0yZ1a2b3c",
        MUSIC_DIR=tmp_sandbox.root / "music",
        TMP_DIR=tmp_sandbox.root / "tmp",
        MAX_CONCURRENT_DOWNLOADS=2,
    )

    mock_resolver = MagicMock()
    # Два трека с одинаковым track_number и одинаковым title
    tracks = [
        ResolvedTrack(
            track_id="1",
            track_number=1,
            title="Intro",
            artist="Artist One",
            audio_source_url="https://soundcloud.com/artist/intro-1",
        ),
        ResolvedTrack(
            track_id="2",
            track_number=1,  # намеренное совпадение
            title="Intro",   # намеренное совпадение
            artist="Artist Two",
            audio_source_url="https://soundcloud.com/artist/intro-2",
        ),
    ]
    mock_resolver.resolve = AsyncMock(
        return_value=({"album": "Same Tracks Album", "album_artist": "Various"}, tracks)
    )

    mock_deezer = MagicMock()
    mock_deezer.download_cover_bytes = AsyncMock(return_value=None)

    download_service = DownloadService(
        settings=settings,
        tmp_sandbox=tmp_sandbox,
        source_resolver=mock_resolver,
        deezer_client=mock_deezer,
    )

    async def fake_download_raw(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.opus"
        raw_file.write_bytes(b"FAKE_AUDIO")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"FAKE_MP3_DATA")

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download_raw),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
    ):
        job = await download_service.create_job(
            url="https://soundcloud.com/artist/sets/duplicates",
            source="soundcloud",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        assert job.completed_tracks == 2

        # Оба файла должны существовать и иметь разные уникальные имена
        file1 = tmp_sandbox.resolve(f"{job.id}/converted/01. Intro.mp3")
        file2 = tmp_sandbox.resolve(f"{job.id}/converted/01. Intro (2).mp3")
        assert file1.exists()
        assert file2.exists()

        # Теги каждого файла должны соответствовать своему треку
        t1_tags = ID3(str(file1))
        t2_tags = ID3(str(file2))
        assert t1_tags.getall("TPE1")[0].text == ["Artist One"]
        assert t2_tags.getall("TPE1")[0].text == ["Artist Two"]


def test_write_draft_tags_zero_byte_cover_and_v1_removal(tmp_sandbox):
    """R2, R3: Проверка защиты от пустых APIC фреймов при 0-байтовой обложке и удаления ID3v1 тегов."""
    from app.config import Settings
    from app.services.download_service import DownloadService

    settings = Settings(
        SECRET_KEY="test_secret_key_fixed_length_32_bytes!!",
        ADMIN_PASSWORD_HASH="$2b$12$e8Y5t1hWzC181zK0m7oKeej1eM0.7tH2l4aG5p8fW9xK0yZ1a2b3c",
        MUSIC_DIR=tmp_sandbox.root / "music",
        TMP_DIR=tmp_sandbox.root / "tmp",
    )

    download_service = DownloadService(
        settings=settings,
        tmp_sandbox=tmp_sandbox,
        source_resolver=MagicMock(),
        deezer_client=MagicMock(),
    )

    mp3_path = tmp_sandbox.resolve("test_tags.mp3")
    mp3_path.write_bytes(b"\xff\xfb\x90\x44" + b"\x00" * 1024)

    # Записываем фейковый ID3v1 в конец файла (128 байт: TAG...)
    v1_tag = b"TAG" + b"Old Title".ljust(30, b"\x00") + b"Old Artist".ljust(30, b"\x00") + b"\x00" * 65
    mp3_path.write_bytes(mp3_path.read_bytes() + v1_tag)
    assert mp3_path.read_bytes()[-128:-125] == b"TAG"

    # Создаем 0-байтовый файл обложки
    zero_cover = tmp_sandbox.resolve("zero_cover.jpg")
    zero_cover.write_bytes(b"")

    track = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="Valid Title",
        artist="Valid Artist",
    )

    download_service._write_draft_tags(
        mp3_path=mp3_path,
        track=track,
        cover_file=zero_cover,
    )

    tags = ID3(str(mp3_path))
    # APIC тег НЕ должен быть добавлен для 0-байтовой обложки
    assert len(tags.getall("APIC")) == 0

    # ID3v1 тег должен быть удален (v1=0)
    assert not mp3_path.read_bytes().endswith(v1_tag)
    assert mp3_path.read_bytes()[-128:-125] != b"TAG"


@pytest.mark.asyncio
async def test_ensure_jpeg_cover_webp_conversion():
    """R2: Конвертация WebP обложки в JPEG через ffmpeg с сохранением совместимости с ID3 APIC."""
    # 1. Пустые или не-WebP данные проходят без изменений
    assert await DownloadService._ensure_jpeg_cover(b"") == b""
    jpeg_dummy = b"\xff\xd8\xff\xe0" + b"\x00" * 32
    png_dummy = b"\x89PNG\r\n\x1a\n" + b"\x00" * 32
    assert await DownloadService._ensure_jpeg_cover(jpeg_dummy) == jpeg_dummy
    assert await DownloadService._ensure_jpeg_cover(png_dummy) == png_dummy

    # 2. Настоящий WebP конвертируется в JPEG (начинающийся с \xff\xd8\xff)
    create_proc = await asyncio.create_subprocess_exec(
        "ffmpeg",
        "-y",
        "-f",
        "lavfi",
        "-i",
        "color=c=purple:s=16x16:d=1",
        "-vframes",
        "1",
        "-c:v",
        "libwebp",
        "-f",
        "webp",
        "pipe:1",
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    webp_bytes, _ = await create_proc.communicate()
    assert webp_bytes.startswith(b"RIFF") and webp_bytes[8:12] == b"WEBP"

    converted_jpeg = await DownloadService._ensure_jpeg_cover(webp_bytes)
    assert converted_jpeg.startswith(b"\xff\xd8\xff")


@pytest.mark.asyncio
async def test_download_pipeline_webp_artwork_converted_and_tagged(tmp_sandbox):
    """R2: WebP обложка SoundCloud перекодируется в JPEG, сохраняется в cover.jpg и вшивается в APIC."""
    from app.config import Settings
    from app.services.download_service import DownloadService

    settings = Settings(
        SECRET_KEY="test_secret_key_fixed_length_32_bytes!!",
        ADMIN_PASSWORD_HASH="$2b$12$e8Y5t1hWzC181zK0m7oKeej1eM0.7tH2l4aG5p8fW9xK0yZ1a2b3c",
        MUSIC_DIR=tmp_sandbox.root / "music",
        TMP_DIR=tmp_sandbox.root / "tmp",
    )

    # Генерируем реальный WebP для теста
    create_proc = await asyncio.create_subprocess_exec(
        "ffmpeg",
        "-y",
        "-f",
        "lavfi",
        "-i",
        "color=c=green:s=16x16:d=1",
        "-vframes",
        "1",
        "-c:v",
        "libwebp",
        "-f",
        "webp",
        "pipe:1",
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    real_webp, _ = await create_proc.communicate()

    mock_resolver = MagicMock()
    track = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="WebP Track",
        artist="WebP Artist",
        album="WebP Album",
        album_artist="WebP Artist",
        cover_url="https://i1.sndcdn.com/artworks-webp-t500x500.webp",
        audio_source_url="https://soundcloud.com/artist/webp-track",
    )
    mock_resolver.resolve = AsyncMock(
        return_value=(
            {"album": "WebP Album", "album_artist": "WebP Artist", "cover_url": "https://i1.sndcdn.com/artworks-webp-t500x500.webp"},
            [track],
        )
    )

    mock_deezer = MagicMock()
    mock_deezer.download_cover_bytes = AsyncMock(return_value=real_webp)

    download_service = DownloadService(
        settings=settings,
        tmp_sandbox=tmp_sandbox,
        source_resolver=mock_resolver,
        deezer_client=mock_deezer,
    )

    async def fake_download_raw(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.opus"
        raw_file.write_bytes(b"FAKE_RAW_AUDIO")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        # Пишем минимальный валидный MP3 заголовок
        output_mp3.write_bytes(b"\xff\xfb\x90\x64" + b"\x00" * 128)

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download_raw),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
    ):
        job = await download_service.create_job(
            url="https://soundcloud.com/artist/webp-track",
            source="soundcloud",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        assert job.cover_relative_path == f"{job.id}/cover.jpg"

        # Файл обложки задачи должен быть валидным JPEG
        cover_path = tmp_sandbox.resolve(f"{job.id}/cover.jpg")
        assert cover_path.exists()
        assert cover_path.read_bytes().startswith(b"\xff\xd8\xff")

        # В MP3 файле должен быть APIC с mime="image/jpeg" и JPEG данными
        mp3_path = tmp_sandbox.resolve(f"{job.id}/converted/01. WebP Track.mp3")
        tags = ID3(str(mp3_path))
        apics = tags.getall("APIC")
        assert len(apics) == 1
        assert apics[0].mime == "image/jpeg"
        assert apics[0].data.startswith(b"\xff\xd8\xff")


@pytest.mark.asyncio
async def test_download_pipeline_track_cover_cleanup_on_exception(tmp_sandbox):
    """R2, R3: Временный файл обложки в raw/ гарантированно удаляется при ошибке тегирования трека."""
    from app.config import Settings
    from app.services.download_service import DownloadService

    settings = Settings(
        SECRET_KEY="test_secret_key_fixed_length_32_bytes!!",
        ADMIN_PASSWORD_HASH="$2b$12$e8Y5t1hWzC181zK0m7oKeej1eM0.7tH2l4aG5p8fW9xK0yZ1a2b3c",
        MUSIC_DIR=tmp_sandbox.root / "music",
        TMP_DIR=tmp_sandbox.root / "tmp",
    )

    track1 = ResolvedTrack(
        track_id="1",
        track_number=1,
        title="Failing Track",
        artist="Artist",
        cover_url="https://i1.sndcdn.com/artworks-track-t500x500.jpg",
        audio_source_url="https://soundcloud.com/artist/track",
    )

    mock_resolver = MagicMock()
    mock_resolver.resolve = AsyncMock(
        return_value=(
            {"album": "Set", "album_artist": "Artist", "cover_url": "https://i1.sndcdn.com/artworks-pl.jpg"},
            [track1],
        )
    )

    mock_deezer = MagicMock()
    mock_deezer.download_cover_bytes = AsyncMock(return_value=b"DUMMY_COVER_BYTES")

    download_service = DownloadService(
        settings=settings,
        tmp_sandbox=tmp_sandbox,
        source_resolver=mock_resolver,
        deezer_client=mock_deezer,
    )

    async def fake_download_raw(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.opus"
        raw_file.write_bytes(b"FAKE_RAW_AUDIO")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"\xff\xfb\x90\x64" + b"\x00" * 128)

    def failing_write_tags(mp3_path, track, cover_file):
        raise RuntimeError("Tagging unexpected failure")

    with (
        patch.object(download_service, "_download_raw_stream", side_effect=fake_download_raw),
        patch.object(download_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(download_service, "_write_draft_tags", side_effect=failing_write_tags),
    ):
        job = await download_service.create_job(
            url="https://soundcloud.com/artist/track",
            source="soundcloud",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.ERROR

        # В каталоге raw не должно остаться никаких файлов (ни cover, ни audio)
        raw_dir = tmp_sandbox.resolve(f"{job.id}/raw")
        assert raw_dir.exists()
        assert len(list(raw_dir.iterdir())) == 0


@pytest.mark.asyncio
async def test_torrent_pipeline_success(tmp_sandbox):
    """Проверка успешного прохождения пайплайна загрузки торрента в DownloadService."""
    from app.config import Settings
    from app.services.download_service import DownloadService

    settings = Settings(
        SECRET_KEY="test_secret_key_fixed_length_32_bytes!!",
        ADMIN_PASSWORD_HASH="$2b$12$e8Y5t1hWzC181zK0m7oKeej1eM0.7tH2l4aG5p8fW9xK0yZ1a2b3c",
        MUSIC_DIR=tmp_sandbox.root / "music",
        TMP_DIR=tmp_sandbox.root / "tmp",
        MAX_CONCURRENT_DOWNLOADS=1,
    )

    mock_resolver = MagicMock()
    mock_deezer = MagicMock()
    mock_torrent = MagicMock()

    # Задаем поведение мока торрента
    async def fake_download(magnet_or_torrent_path, output_dir, on_progress=None):
        if on_progress:
            on_progress(100, "10MiB", "15", "0s")
        # Создаем фиктивный аудиофайл
        (output_dir / "01. Test Track.flac").write_bytes(b"FLAC_DATA")

    mock_torrent.download_torrent = AsyncMock(side_effect=fake_download)

    fake_tracks = [
        ResolvedTrack(
            track_id="torrent_1",
            track_number=1,
            title="Test Track",
            artist="Test Artist",
            album="Test Album",
            album_artist="Test Artist",
            year=2024,
            genre="Electronic",
            duration=180,
            audio_source_url="",
        )
    ]
    mock_torrent.inspect_and_prepare = AsyncMock(
        return_value=(
            {"album": "Test Album", "album_artist": "Test Artist", "year": 2024, "cover_url": None},
            fake_tracks,
        )
    )

    svc = DownloadService(
        settings=settings,
        tmp_sandbox=tmp_sandbox,
        source_resolver=mock_resolver,
        deezer_client=mock_deezer,
        torrent_service=mock_torrent,
    )

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"MP3_DATA")

    with (
        patch.object(svc, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(svc, "_write_draft_tags"),
    ):
        job = await svc.create_job(
            url="magnet:?xt=urn:btih:d2474e86c95b19b8bcfdb92bc12c9d44667cfa36",
            source="torrent",
        )

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        assert job.status == JobStatus.READY
        assert job.completed_tracks == 1
        assert len(job.tracks) == 1
        assert job.tracks[0].title == "Test Track"


@pytest.mark.asyncio
async def test_torrent_pipeline_service_disabled(tmp_sandbox):
    """Проверка ошибки при запуске торрент-задачи, если TorrentService не передан."""
    from app.config import Settings
    from app.services.download_service import DownloadService

    settings = Settings(
        SECRET_KEY="test_secret_key_fixed_length_32_bytes!!",
        ADMIN_PASSWORD_HASH="$2b$12$e8Y5t1hWzC181zK0m7oKeej1eM0.7tH2l4aG5p8fW9xK0yZ1a2b3c",
        MUSIC_DIR=tmp_sandbox.root / "music",
        TMP_DIR=tmp_sandbox.root / "tmp",
        MAX_CONCURRENT_DOWNLOADS=1,
    )

    svc = DownloadService(
        settings=settings,
        tmp_sandbox=tmp_sandbox,
        source_resolver=MagicMock(),
        deezer_client=MagicMock(),
        torrent_service=None,  # отключен
    )

    job = await svc.create_job(
        url="magnet:?xt=urn:btih:d2474e86c95b19b8bcfdb92bc12c9d44667cfa36",
        source="torrent",
    )

    for _ in range(50):
        if job.status in (JobStatus.READY, JobStatus.ERROR):
            break
        await asyncio.sleep(0.05)

    assert job.status == JobStatus.ERROR
    assert "Поддержка торрентов отключена" in str(job.error)







