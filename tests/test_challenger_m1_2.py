"""Empirical verification and stress-testing suite by Challenger 2 for Milestone 1.

Focus areas:
1. Normal downloads priority: use_cookies=False, extractor-args ios,visionos, no --cookies.
2. Age Gate fallback: Detection oracle, leftover .part cleanup, second execution with --cookies.
3. Non-age error fast-fail: Video unavailable, 403, 404, rate limits do NOT retry cookies.
4. Pipeline status='error' behavior: Skipping errored tracks, progress budgeting, completed_tracks count,
   get_job_tracks response, and edge case handling.
"""

import asyncio
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.config import Settings
from app.core.exceptions import DownloadError
from app.core.sandbox import PathSandbox
from app.schemas.download import JobStatus, ResolvedTrack
from app.services.deezer_client import DeezerClient
from app.services.download_service import DownloadService, is_age_restricted_error
from app.services.source_resolver import SourceResolver


@pytest.fixture
def challenger_service(test_settings: Settings, tmp_sandbox: PathSandbox) -> DownloadService:
    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.download_cover_bytes = AsyncMock(return_value=b"test_cover_bytes")

    mock_resolver = MagicMock(spec=SourceResolver)
    mock_resolver.detect_source.return_value = "deezer"

    return DownloadService(
        settings=test_settings,
        tmp_sandbox=tmp_sandbox,
        source_resolver=mock_resolver,
        deezer_client=mock_deezer,
    )


# ---------------------------------------------------------------------------
# 1. Verification of Guest Mode (Normal Downloads)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_guest_mode_priority_with_cookies_present(
    challenger_service: DownloadService,
    tmp_path: Path,
):
    """Normal download must execute in fast guest mode without --cookies even if cookies file exists."""
    cookies_path = tmp_path / "cookies.txt"
    cookies_path.write_text("# Netscape HTTP Cookie File\n.youtube.com TRUE / FALSE 12345 SID test\n")
    challenger_service._settings.YTDLP_COOKIES_PATH = cookies_path

    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)
    out_file = raw_dir / "01.opus"
    out_file.write_bytes(b"dummy_stream_audio")

    mock_proc = AsyncMock()
    mock_proc.returncode = 0
    mock_proc.communicate.return_value = (b"", b"")
    mock_proc.stdout.readline = AsyncMock(return_value=b"")
    mock_proc.stderr.readline = AsyncMock(return_value=b"")
    mock_proc.wait = AsyncMock(return_value=0)

    with patch("asyncio.create_subprocess_exec", return_value=mock_proc) as mock_exec:
        result = await challenger_service._download_raw_stream(
            source_url="https://www.youtube.com/watch?v=normal_video_123",
            output_template=raw_dir / "%(title)s.%(ext)s",
            raw_dir=raw_dir,
            track_idx=1,
        )

        assert result == out_file
        assert mock_exec.call_count == 1
        cmd_args = mock_exec.call_args[0]
        assert "--cookies" not in cmd_args
        assert "--extractor-args" in cmd_args
        ext_idx = cmd_args.index("--extractor-args")
        assert cmd_args[ext_idx + 1] == "youtube:player_client=ios,visionos"
        assert cmd_args[-1] == "https://www.youtube.com/watch?v=normal_video_123"


@pytest.mark.asyncio
async def test_guest_mode_without_cookies_configured(
    challenger_service: DownloadService,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
):
    """Normal download when no cookies are configured must use guest mode cleanly."""
    empty_root = tmp_path / "empty_root"
    empty_root.mkdir(parents=True, exist_ok=True)
    monkeypatch.setattr("app.config.PROJECT_ROOT", empty_root)
    monkeypatch.setattr(Path, "cwd", lambda: empty_root)
    challenger_service._settings.YTDLP_COOKIES_PATH = None

    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)
    out_file = raw_dir / "03.webm"
    out_file.write_bytes(b"dummy_stream_audio_3")

    mock_proc = AsyncMock()
    mock_proc.returncode = 0
    mock_proc.communicate.return_value = (b"", b"")
    mock_proc.stdout.readline = AsyncMock(return_value=b"")
    mock_proc.stderr.readline = AsyncMock(return_value=b"")
    mock_proc.wait = AsyncMock(return_value=0)

    with patch("asyncio.create_subprocess_exec", return_value=mock_proc) as mock_exec:
        result = await challenger_service._download_raw_stream(
            source_url="https://www.youtube.com/watch?v=guest_only_456",
            output_template=raw_dir / "%(title)s.%(ext)s",
            raw_dir=raw_dir,
            track_idx=3,
        )

        assert result == out_file
        assert mock_exec.call_count == 1
        cmd_args = mock_exec.call_args[0]
        assert "--cookies" not in cmd_args
        assert "--extractor-args" in cmd_args
        assert "youtube:player_client=ios,visionos" in cmd_args


# ---------------------------------------------------------------------------
# 2. Age Gate Detection, .part Cleanup, and Cookies Fallback
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("err_text", "expected_is_age"),
    [
        ("ERROR: [youtube] Sign in to confirm your age", True),
        ("ERROR: This video is age-restricted and only available to registered users", True),
        ("Sign in to confirm your age", True),
        ("sign in to view this video", True),
        ("This content requires authentication to view", True),
        ("login required to stream media", True),
        ("SIGN IN TO CONFIRM YOUR AGE", True),
        ("THIS VIDEO IS AGE-RESTRICTED", True),
        ("ERROR: [youtube] Video unavailable", False),
        ("ERROR: Private video. Sign in if you've been granted access", False),
        ("HTTP Error 403: Forbidden", False),
        ("HTTP Error 404: Not Found", False),
        ("HTTP Error 429: Too Many Requests", False),
        ("The page needs to be reloaded", False),
        ("", False),
    ],
)
def test_is_age_restricted_error_oracle(err_text: str, expected_is_age: bool):
    """Exhaustive check of Age Gate pattern detection."""
    assert is_age_restricted_error(err_text) is expected_is_age
    assert DownloadService.is_age_restricted_error(err_text) is expected_is_age


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "age_error_msg",
    [
        "ERROR: [youtube] Sign in to confirm your age",
        "ERROR: This video is age-restricted and only available to registered users",
        "Sign in to view this video",
        "This video requires authentication",
        "Login required to watch this video",
    ],
)
async def test_age_gate_fallback_triggers_cleanup_and_cookies_retry(
    challenger_service: DownloadService,
    tmp_path: Path,
    age_error_msg: str,
):
    """Age gate error must clean up .part files of the current track and retry with cookies."""
    cookies_path = tmp_path / "cookies.txt"
    cookies_path.write_text("# Netscape HTTP Cookie File\n")
    challenger_service._settings.YTDLP_COOKIES_PATH = cookies_path

    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)

    # Residual part files for track 1 (must be deleted)
    part_file_1a = raw_dir / "01.webm.part"
    part_file_1b = raw_dir / "01.f140.part"
    part_file_1a.write_bytes(b"partial content A")
    part_file_1b.write_bytes(b"partial content B")

    # Residual part file for track 2 (must NOT be touched!)
    part_file_2 = raw_dir / "02.webm.part"
    part_file_2.write_bytes(b"partial content track 2")

    # Unrelated file in directory (must NOT be touched!)
    unrelated_file = raw_dir / "unrelated.log"
    unrelated_file.write_bytes(b"log data")

    # Success file after 2nd attempt
    out_file = raw_dir / "01.webm"
    out_file.write_bytes(b"completed_audio")

    proc_guest = AsyncMock()
    proc_guest.returncode = 1
    proc_guest.communicate.return_value = (b"", age_error_msg.encode())
    proc_guest.stdout.readline = AsyncMock(return_value=b"")
    proc_guest.stderr.readline = AsyncMock(side_effect=[f"{age_error_msg}\n".encode(), b""])
    proc_guest.wait = AsyncMock(return_value=1)

    proc_cookies = AsyncMock()
    proc_cookies.returncode = 0
    proc_cookies.communicate.return_value = (b"", b"")
    proc_cookies.stdout.readline = AsyncMock(return_value=b"")
    proc_cookies.stderr.readline = AsyncMock(return_value=b"")
    proc_cookies.wait = AsyncMock(return_value=0)

    with patch("asyncio.create_subprocess_exec", side_effect=[proc_guest, proc_cookies]) as mock_exec:
        result = await challenger_service._download_raw_stream(
            source_url="https://www.youtube.com/watch?v=age_gated_1",
            output_template=raw_dir / "%(title)s.%(ext)s",
            raw_dir=raw_dir,
            track_idx=1,
        )

        assert result == out_file
        assert mock_exec.call_count == 2

        # 1st call: guest mode, no cookies
        args_1 = mock_exec.call_args_list[0][0]
        assert "--cookies" not in args_1
        assert "youtube:player_client=ios,visionos" in args_1

        # 2nd call: cookies passed
        args_2 = mock_exec.call_args_list[1][0]
        assert "--cookies" in args_2
        assert str(cookies_path.resolve()) in args_2
        assert "--extractor-args" not in args_2

        # Verify targeted cleanup: track 1 part files deleted
        assert not part_file_1a.exists()
        assert not part_file_1b.exists()

        # Verify safety: track 2 part file and unrelated file preserved
        assert part_file_2.exists()
        assert unrelated_file.exists()


@pytest.mark.asyncio
async def test_age_gate_without_cookies_does_not_retry(
    challenger_service: DownloadService,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
):
    """When Age Gate occurs but no cookies file exists, must fail immediately without second call."""
    empty_root = tmp_path / "empty_root"
    empty_root.mkdir(parents=True, exist_ok=True)
    monkeypatch.setattr("app.config.PROJECT_ROOT", empty_root)
    monkeypatch.setattr(Path, "cwd", lambda: empty_root)
    challenger_service._settings.YTDLP_COOKIES_PATH = None

    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)
    part_file = raw_dir / "01.opus.part"
    part_file.write_bytes(b"some bytes")

    proc = AsyncMock()
    proc.returncode = 1
    err_msg = "ERROR: [youtube] Sign in to confirm your age"
    proc.communicate.return_value = (b"", err_msg.encode())
    proc.stdout.readline = AsyncMock(return_value=b"")
    proc.stderr.readline = AsyncMock(side_effect=[f"{err_msg}\n".encode(), b""])
    proc.wait = AsyncMock(return_value=1)

    with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
        with pytest.raises(DownloadError, match="confirm your age"):
            await challenger_service._download_raw_stream(
                source_url="https://www.youtube.com/watch?v=age_no_cookie",
                output_template=raw_dir / "%(title)s.%(ext)s",
                raw_dir=raw_dir,
                track_idx=1,
            )

        assert mock_exec.call_count == 1


# ---------------------------------------------------------------------------
# 3. Non-Age Error Handling (Fast Fail)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "non_age_error",
    [
        "ERROR: [youtube] Video unavailable (404 Not Found)",
        "ERROR: Private video. Sign in if you've been granted access",
        "HTTP Error 403: Forbidden",
        "HTTP Error 429: Too Many Requests",
        "ffmpeg error: audio stream corrupted",
        "ERROR: Unable to extract video data",
    ],
)
async def test_non_age_errors_fast_fail_without_cookies_retry(
    challenger_service: DownloadService,
    tmp_path: Path,
    non_age_error: str,
):
    """Non-age errors must fail immediately without retrying cookies, even when cookies file exists."""
    cookies_path = tmp_path / "cookies.txt"
    cookies_path.write_text("# Netscape HTTP Cookie File\n")
    challenger_service._settings.YTDLP_COOKIES_PATH = cookies_path

    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)
    part_file = raw_dir / "01.part"
    part_file.write_bytes(b"unfinished")

    proc = AsyncMock()
    proc.returncode = 1
    proc.communicate.return_value = (b"", non_age_error.encode())
    proc.stdout.readline = AsyncMock(return_value=b"")
    proc.stderr.readline = AsyncMock(side_effect=[f"{non_age_error}\n".encode(), b""])
    proc.wait = AsyncMock(return_value=1)

    with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
        with pytest.raises(DownloadError):
            await challenger_service._download_raw_stream(
                source_url="https://www.youtube.com/watch?v=broken_track",
                output_template=raw_dir / "%(title)s.%(ext)s",
                raw_dir=raw_dir,
                track_idx=1,
            )

        # Must execute exactly once - NO retry
        assert mock_exec.call_count == 1
        # Part file was not deleted since no retry was initiated
        assert part_file.exists()


@pytest.mark.asyncio
async def test_download_raw_stream_timeout_handling(
    challenger_service: DownloadService,
    tmp_path: Path,
):
    """Subprocess timeout must kill the process and raise DownloadError."""
    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)

    mock_proc = AsyncMock()
    mock_proc.kill = MagicMock()
    mock_proc.wait = AsyncMock(side_effect=asyncio.TimeoutError())
    mock_proc.stdout = None
    mock_proc.stderr = None

    with patch("asyncio.create_subprocess_exec", return_value=mock_proc):
        with pytest.raises(DownloadError, match="таймаут загрузки"):
            await challenger_service._download_raw_stream(
                source_url="https://www.youtube.com/watch?v=hangs_forever",
                output_template=raw_dir / "%(title)s.%(ext)s",
                raw_dir=raw_dir,
                track_idx=1,
            )

        mock_proc.kill.assert_called_once()


# ---------------------------------------------------------------------------
# 4. Pipeline Behavior with status='error' Tracks
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_pipeline_multi_track_with_mixed_error_tracks(
    challenger_service: DownloadService,
):
    """Verify pipeline gracefully handles mixed success and error tracks in an album job."""
    tracks = [
        ResolvedTrack(
            track_id="t1",
            track_number=1,
            title="Track One (Success)",
            artist="Artist",
            album="Album",
            album_artist="Artist",
            duration=120,
            audio_source_url="https://youtube.com/watch?v=t1",
            status="pending",
        ),
        ResolvedTrack(
            track_id="t2",
            track_number=2,
            title="Track Two (Matched Error)",
            artist="Artist",
            album="Album",
            album_artist="Artist",
            duration=180,
            audio_source_url="",
            status="error",
            error="AudioMatchError: No candidates found",
        ),
        ResolvedTrack(
            track_id="t3",
            track_number=3,
            title="Track Three (Missing URL but status pending)",
            artist="Artist",
            album="Album",
            album_artist="Artist",
            duration=200,
            audio_source_url="",
            status="pending",
        ),
        ResolvedTrack(
            track_id="t4",
            track_number=4,
            title="Track Four (Success)",
            artist="Artist",
            album="Album",
            album_artist="Artist",
            duration=240,
            audio_source_url="https://youtube.com/watch?v=t4",
            status="pending",
        ),
    ]

    challenger_service._resolver.resolve = AsyncMock(
        return_value=(
            {"album": "Album", "album_artist": "Artist", "year": 2024, "genre": "Rock"},
            tracks,
        )
    )

    downloaded_indices: list[int] = []
    transcoded_files: list[str] = []

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        downloaded_indices.append(track_idx)
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        transcoded_files.append(output_mp3.name)
        output_mp3.write_bytes(b"mp3_data")

    with (
        patch.object(challenger_service, "_download_raw_stream", side_effect=fake_download),
        patch.object(challenger_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(challenger_service, "_write_draft_tags", return_value=None),
    ):
        job = await challenger_service.create_job("https://deezer.com/album/mixed")

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        # 1. Job completed to READY
        assert job.status == JobStatus.READY
        assert job.progress == 100.0

        # 2. Only tracks 1 and 4 were downloaded and transcoded
        assert downloaded_indices == [1, 4]
        assert len(transcoded_files) == 2
        assert "01. Track One (Success).mp3" in transcoded_files
        assert "04. Track Four (Success).mp3" in transcoded_files

        # 3. Completed tracks count reflects only successful tracks (2 of 4)
        assert job.completed_tracks == 2

        # 4. Individual track statuses
        assert tracks[0].status == "ready"
        assert tracks[0].converted_path is not None
        assert tracks[1].status == "error"
        assert tracks[1].converted_path is None
        assert tracks[2].status == "pending"  # skipped due to empty audio_source_url
        assert tracks[2].converted_path is None
        assert tracks[3].status == "ready"
        assert tracks[3].converted_path is not None

        # 5. get_job_tracks returns all 4 tracks
        tracks_resp = challenger_service.get_job_tracks(job.id)
        assert len(tracks_resp.tracks) == 4
        assert tracks_resp.tracks[1].status == "error"
        assert tracks_resp.tracks[1].error == "AudioMatchError: No candidates found"


@pytest.mark.asyncio
async def test_pipeline_all_tracks_have_status_error(
    challenger_service: DownloadService,
):
    """Examine behavior when all tracks in a job have status='error'."""
    tracks = [
        ResolvedTrack(
            track_id="t1",
            track_number=1,
            title="Fail 1",
            artist="Artist",
            album="Album",
            album_artist="Artist",
            duration=120,
            audio_source_url="",
            status="error",
            error="Matching error 1",
        ),
        ResolvedTrack(
            track_id="t2",
            track_number=2,
            title="Fail 2",
            artist="Artist",
            album="Album",
            album_artist="Artist",
            duration=120,
            audio_source_url="",
            status="error",
            error="Matching error 2",
        ),
    ]

    challenger_service._resolver.resolve = AsyncMock(
        return_value=(
            {"album": "Album", "album_artist": "Artist"},
            tracks,
        )
    )

    with (
        patch.object(challenger_service, "_download_raw_stream", new=AsyncMock()) as mock_dl,
        patch.object(challenger_service, "_transcode_to_mp3", new=AsyncMock()) as mock_tc,
    ):
        job = await challenger_service.create_job("https://deezer.com/album/all_fail")

        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.05)

        # Neither download nor transcode was invoked
        assert mock_dl.call_count == 0
        assert mock_tc.call_count == 0

        # Note: completed_tracks is 0
        assert job.completed_tracks == 0
        assert job.status == JobStatus.READY
        assert job.progress == 100.0

        # Both tracks remain error
        assert all(t.status == "error" for t in job.tracks)
