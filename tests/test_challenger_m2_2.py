"""Empirical verification and stress-testing suite by Challenger 2 for Milestone 2.

Focus areas:
1. Monotonic multi-track progress calculation for a 12-track album download with severe fluctuating
   percentages (simulating packet drops, chunk retries, and out-of-order progress updates).
2. Verification that job.progress never decreases at ANY step (dt >= 0 invariant) and smoothly
   progresses from 0.0 to 100.0.
3. Verification that job.current_step format accurately reflects track index, total (12),
   exact track title, and rounded track percentage.
4. Edge cases: extreme percentage clamping (< 0%, > 100%), stochastic fluctuations (1000+ points),
   mixed errored tracks in 12-track album, and single-track albums.
"""

import asyncio
import random
import re
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.config import Settings
from app.core.sandbox import PathSandbox
from app.schemas.download import JobStatus, ResolvedTrack
from app.services.deezer_client import DeezerClient
from app.services.download_service import DownloadJob, DownloadService
from app.services.source_resolver import SourceResolver


@pytest.fixture
def challenger_service(test_settings: Settings, tmp_sandbox: PathSandbox) -> DownloadService:
    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.download_cover_bytes = AsyncMock(return_value=b"fake_cover_image_bytes")

    mock_resolver = MagicMock(spec=SourceResolver)
    mock_resolver.detect_source.return_value = "deezer"

    return DownloadService(
        settings=test_settings,
        tmp_sandbox=tmp_sandbox,
        source_resolver=mock_resolver,
        deezer_client=mock_deezer,
    )


# ---------------------------------------------------------------------------
# 1. Primary Empirical Mission: 12-Track Album with Fluctuating Percentages
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_simulate_12_track_album_download_fluctuating_percentages(
    challenger_service: DownloadService,
    tmp_path: Path,
):
    """Simulate a 12-track album download where each track receives fluctuating percentages.

    Verifies:
    - job.progress never decreases across all steps (strictly monotonic non-decreasing).
    - job.progress transitions smoothly from 0.0 to 100.0.
    - job.current_step accurately reflects track index, total (12), title, and track percentage.
    """
    total_tracks = 12
    tracklist: list[ResolvedTrack] = []
    for idx in range(1, total_tracks + 1):
        tracklist.append(
            ResolvedTrack(
                track_id=f"track_{idx}",
                track_number=idx,
                title=f"Symphony Track {idx:02d} - Theme",
                artist="Empirical Orchestra",
                duration=200 + idx,
                audio_source_url=f"https://youtube.com/watch?v=sample_{idx:02d}",
            )
        )

    challenger_service._resolver.resolve = AsyncMock(
        return_value=(
            {"album": "Empirical Symphony in 12 Movements", "album_artist": "Empirical Orchestra"},
            tracklist,
        )
    )

    # Fluctuating percentage profiles for all 12 tracks simulating chunk drops, retries, stalls
    track_fluctuation_profiles: dict[int, list[float]] = {
        1: [0.0, 10.0, 25.0, 12.0, 40.0, 30.0, 70.0, 50.0, 95.0, 80.0, 100.0],
        2: [0.0, 85.0, 15.0, 90.0, 40.0, 100.0],
        3: [0.0, 5.0, 2.0, 15.0, 8.0, 30.0, 20.0, 50.0, 40.0, 75.0, 60.0, 99.0, 90.0, 100.0],
        4: [0.0, 0.0, 0.0, 100.0],  # sudden jump
        5: [0.0, 33.3, 11.1, 55.5, 22.2, 77.7, 44.4, 99.9, 100.0],
        6: [0.0, 50.0, 10.0, 80.0, 30.0, 95.0, 50.0, 100.0],
        7: [0.0, 99.0, 1.0, 100.0],  # severe drop from 99% to 1% then 100%
        8: [0.0, 14.7, 7.3, 29.4, 18.2, 58.8, 35.1, 88.2, 70.0, 100.0],
        9: [0.0, 10.0, 5.0, 20.0, 15.0, 30.0, 25.0, 40.0, 35.0, 50.0, 45.0, 60.0, 55.0, 70.0, 65.0, 80.0, 75.0, 90.0, 85.0, 100.0],
        10: [0.0, 100.0],  # single step
        11: [0.0, 45.0, 20.0, 60.0, 35.0, 80.0, 55.0, 95.0, 90.0, 100.0],
        12: [0.0, 12.5, 6.2, 25.0, 18.7, 50.0, 37.5, 75.0, 62.5, 87.5, 80.0, 100.0],
    }

    # State timeline recording every progress and step event
    history: list[dict[str, object]] = []

    def record_state(job, context: str):
        history.append({
            "context": context,
            "status": job.status,
            "progress": job.progress,
            "current_step": job.current_step,
            "completed_tracks": job.completed_tracks,
        })

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"dummy_raw_stream")
        job = next(iter(challenger_service._jobs.values()))
        record_state(job, f"download_start_{track_idx}")

        if on_progress:
            percentages = track_fluctuation_profiles.get(track_idx, [0.0, 50.0, 100.0])
            for p in percentages:
                on_progress(p)
                record_state(job, f"download_progress_{track_idx}_{p}")
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"fake_mp3_bytes")
        job = next(iter(challenger_service._jobs.values()))
        record_state(job, "transcode")

    def fake_tags(mp3_path, track, cover_file):
        job = next(iter(challenger_service._jobs.values()))
        record_state(job, "tags")

    with (
        patch.object(challenger_service, "_download_raw_stream", side_effect=fake_download),
        patch.object(challenger_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(challenger_service, "_write_draft_tags", side_effect=fake_tags),
    ):
        job = await challenger_service.create_job(
            url="https://www.deezer.com/album/1234567890",
            source="deezer",
        )
        record_state(job, "job_created")

        # Wait for background task to complete
        for _ in range(100):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.02)
        record_state(job, "job_finished")

    # 1. Final Status and Bounds Check
    assert job.status == JobStatus.READY, f"Expected READY, got {job.status} (error={job.error})"
    assert job.progress == 100.0
    assert job.completed_tracks == 12
    assert job.current_step == "Готово к проверке тегов"

    # 2. Strict Monotonicity Check (progress must never decrease)
    assert len(history) > 100, f"Expected > 100 state recordings, got {len(history)}"
    progress_series = [float(item["progress"]) for item in history]

    for i in range(1, len(progress_series)):
        prev = progress_series[i - 1]
        curr = progress_series[i]
        assert curr >= prev, (
            f"MONOTONICITY VIOLATION at step {i}: progress dropped from {prev} to {curr}! "
            f"Event context: {history[i]['context']}, Step: {history[i]['current_step']}"
        )

    # 3. Verify Progress starts at 0.0 and ends at 100.0
    assert progress_series[0] == 0.0
    assert progress_series[-1] == 100.0
    for val in progress_series:
        assert 0.0 <= val <= 100.0

    # 4. Verify job.current_step Format during Download phase:
    # Format specification: "Загрузка трека {idx}/{total}: {title} ({p:.0f}%)"
    step_regex = re.compile(r"^Загрузка трека (\d+)/12: (.+) \((\d+)%\)$")
    download_step_events = [
        item for item in history if str(item["current_step"]).startswith("Загрузка трека")
    ]
    assert len(download_step_events) >= 12

    for ev in download_step_events:
        step_text = str(ev["current_step"])
        match = step_regex.match(step_text)
        assert match is not None, f"Malformed step text: {step_text!r}"
        matched_idx = int(match.group(1))
        matched_title = match.group(2)
        matched_pct = int(match.group(3))

        assert 1 <= matched_idx <= 12
        assert matched_title == f"Symphony Track {matched_idx:02d} - Theme"
        assert 0 <= matched_pct <= 100

    # 5. Verify job.current_step Format during Conversion phase
    # Format specification: "Конвертация трека {idx}/12: {title} в MP3 320k"
    conv_step_regex = re.compile(r"^Конвертация трека (\d+)/12: (.+) в MP3 320k$")
    conv_step_events = [
        item for item in history if str(item["current_step"]).startswith("Конвертация трека")
    ]
    assert len(conv_step_events) == 12
    for idx, ev in enumerate(conv_step_events, start=1):
        step_text = str(ev["current_step"])
        match = conv_step_regex.match(step_text)
        assert match is not None, f"Malformed conversion step text: {step_text!r}"
        assert int(match.group(1)) == idx
        assert match.group(2) == f"Symphony Track {idx:02d} - Theme"

    # 6. Verify job.current_step Format during Tagging phase
    # Format specification: "Запись ID3v2.4 тегов {idx}/12: {title}"
    tag_step_regex = re.compile(r"^Запись ID3v2.4 тегов (\d+)/12: (.+)$")
    tag_step_events = [
        item for item in history if str(item["current_step"]).startswith("Запись ID3v2.4 тегов")
    ]
    assert len(tag_step_events) == 12
    for idx, ev in enumerate(tag_step_events, start=1):
        step_text = str(ev["current_step"])
        match = tag_step_regex.match(step_text)
        assert match is not None, f"Malformed tagging step text: {step_text!r}"
        assert int(match.group(1)) == idx
        assert match.group(2) == f"Symphony Track {idx:02d} - Theme"


# ---------------------------------------------------------------------------
# 2. Stochastic Stress Test: 1000+ Random Fluctuations across 12 Tracks
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_12_track_stochastic_fluctuating_percentages(
    challenger_service: DownloadService,
    tmp_path: Path,
):
    """Stress test with 1200 random progress perturbations across 12 tracks."""
    rng = random.Random(42)  # Fixed seed for deterministic empirical repeatability
    total_tracks = 12
    tracklist = [
        ResolvedTrack(
            track_id=f"stoch_{idx}",
            track_number=idx,
            title=f"Stochastic Track #{idx}",
            artist="Stress Tester",
            duration=180,
            audio_source_url=f"https://youtube.com/watch?v=stoch_{idx}",
        )
        for idx in range(1, total_tracks + 1)
    ]

    challenger_service._resolver.resolve = AsyncMock(
        return_value=({"album": "Chaos Album", "album_artist": "Stress Tester"}, tracklist)
    )

    recorded_progress: list[float] = []

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"stream")
        job = next(iter(challenger_service._jobs.values()))

        if on_progress:
            # 100 wildly fluctuating points per track
            for _ in range(100):
                random_pct = rng.uniform(0.0, 100.0)
                on_progress(random_pct)
                recorded_progress.append(job.progress)
            # Final completion of stream
            on_progress(100.0)
            recorded_progress.append(job.progress)
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"mp3")
        job = next(iter(challenger_service._jobs.values()))
        recorded_progress.append(job.progress)

    def fake_tags(mp3_path, track, cover_file):
        job = next(iter(challenger_service._jobs.values()))
        recorded_progress.append(job.progress)

    with (
        patch.object(challenger_service, "_download_raw_stream", side_effect=fake_download),
        patch.object(challenger_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(challenger_service, "_write_draft_tags", side_effect=fake_tags),
    ):
        job = await challenger_service.create_job("https://www.deezer.com/album/chaos", "deezer")
        for _ in range(100):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.02)

    assert job.status == JobStatus.READY
    assert job.progress == 100.0
    assert len(recorded_progress) > 1200

    # Ensure no step decreased
    for i in range(1, len(recorded_progress)):
        assert recorded_progress[i] >= recorded_progress[i - 1], (
            f"Stochastic monotonicity failure at index {i}: "
            f"{recorded_progress[i - 1]} > {recorded_progress[i]}"
        )


# ---------------------------------------------------------------------------
# 3. Mixed Errored Tracks in 12-Track Album
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_12_track_album_with_intermittent_track_errors(
    challenger_service: DownloadService,
    tmp_path: Path,
):
    """Verify that when some tracks fail resolution or have error status in a 12-track album,

    job.progress still progresses monotonically to 100.0% and current_step notes skipped tracks.
    """
    total_tracks = 12
    tracklist: list[ResolvedTrack] = []
    # Tracks 4 and 9 have errors
    for idx in range(1, total_tracks + 1):
        if idx in (4, 9):
            tracklist.append(
                ResolvedTrack(
                    track_id=f"err_{idx}",
                    track_number=idx,
                    title=f"Failed Track {idx}",
                    artist="Artist",
                    duration=180,
                    audio_source_url="",
                    status="error",
                    error="Audio stream not found",
                )
            )
        else:
            tracklist.append(
                ResolvedTrack(
                    track_id=f"ok_{idx}",
                    track_number=idx,
                    title=f"Healthy Track {idx}",
                    artist="Artist",
                    duration=180,
                    audio_source_url=f"https://youtube.com/watch?v=healthy_{idx}",
                    status="ok",
                )
            )

    challenger_service._resolver.resolve = AsyncMock(
        return_value=({"album": "Partial Album", "album_artist": "Artist"}, tracklist)
    )

    recorded_progress: list[float] = []
    recorded_steps: list[str] = []

    orig_setattr = DownloadJob.__setattr__

    def hooked_setattr(self, name, value):
        orig_setattr(self, name, value)
        if name == "progress":
            recorded_progress.append(value)
        elif name == "current_step":
            recorded_steps.append(value)

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"stream")
        if on_progress:
            # Fluctuating progress
            for p in [10.0, 50.0, 20.0, 80.0, 100.0]:
                on_progress(p)
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"mp3")

    def fake_tags(mp3_path, track, cover_file):
        pass

    with (
        patch.object(DownloadJob, "__setattr__", hooked_setattr),
        patch.object(challenger_service, "_download_raw_stream", side_effect=fake_download),
        patch.object(challenger_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(challenger_service, "_write_draft_tags", side_effect=fake_tags),
    ):
        job = await challenger_service.create_job("https://www.deezer.com/album/partial", "deezer")
        for _ in range(100):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.02)

    assert job.status == JobStatus.READY
    assert job.progress == 100.0
    assert job.completed_tracks == 10  # 12 total minus 2 errors

    # Check that skipped step was recorded
    assert any("Пропуск трека 4/12: Failed Track 4 (ошибка)" in s for s in recorded_steps)
    assert any("Пропуск трека 9/12: Failed Track 9 (ошибка)" in s for s in recorded_steps)

    # Monotonicity check
    for i in range(1, len(recorded_progress)):
        assert recorded_progress[i] >= recorded_progress[i - 1], (
            f"Drop detected at index {i}: {recorded_progress[i - 1]} -> {recorded_progress[i]}"
        )


# ---------------------------------------------------------------------------
# 4. Clamping and Extreme Values Stress Test
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_extreme_progress_percentage_clamping(
    challenger_service: DownloadService,
    tmp_path: Path,
):
    """Verify that anomalous out-of-range percentages (-50%, 999%) are safely clamped."""
    track = ResolvedTrack(
        track_id="t1",
        track_number=1,
        title="Extreme Track",
        artist="Artist",
        duration=180,
        audio_source_url="https://youtube.com/watch?v=ext1",
    )
    challenger_service._resolver.resolve = AsyncMock(
        return_value=({"album": "Extreme Album", "album_artist": "Artist"}, [track])
    )

    recorded_progress: list[float] = []

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / "01.webm"
        raw_file.write_bytes(b"stream")
        job = next(iter(challenger_service._jobs.values()))
        if on_progress:
            # Negative, absurdly large, and extreme values
            for p in [-100.0, -1.0, 0.0, 50.0, 150.0, 999.0, 100.0]:
                on_progress(p)
                recorded_progress.append(job.progress)
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"mp3")

    def fake_tags(mp3_path, track, cover_file):
        pass

    with (
        patch.object(challenger_service, "_download_raw_stream", side_effect=fake_download),
        patch.object(challenger_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(challenger_service, "_write_draft_tags", side_effect=fake_tags),
    ):
        job = await challenger_service.create_job("https://www.deezer.com/album/extreme", "deezer")
        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.02)

    assert job.status == JobStatus.READY
    assert job.progress == 100.0
    for p in recorded_progress:
        assert 0.0 <= p <= 100.0
    for i in range(1, len(recorded_progress)):
        assert recorded_progress[i] >= recorded_progress[i - 1]


# ---------------------------------------------------------------------------
# 5. Single Track Album Monotonicity
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_single_track_album_progress_monotonicity(
    challenger_service: DownloadService,
    tmp_path: Path,
):
    """Verify single-track download follows monotonic budget from 0 to 100%."""
    track = ResolvedTrack(
        track_id="single_1",
        track_number=1,
        title="Single Hit",
        artist="Artist",
        duration=210,
        audio_source_url="https://youtube.com/watch?v=single",
    )
    challenger_service._resolver.resolve = AsyncMock(
        return_value=({"album": "Single Album", "album_artist": "Artist"}, [track])
    )

    recorded_progress: list[float] = []
    recorded_steps: list[str] = []

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / "01.webm"
        raw_file.write_bytes(b"stream")
        job = next(iter(challenger_service._jobs.values()))
        if on_progress:
            for p in [0.0, 15.0, 10.0, 45.0, 30.0, 85.0, 60.0, 100.0]:
                on_progress(p)
                recorded_progress.append(job.progress)
                recorded_steps.append(job.current_step)
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"mp3")
        job = next(iter(challenger_service._jobs.values()))
        recorded_progress.append(job.progress)
        recorded_steps.append(job.current_step)

    def fake_tags(mp3_path, track, cover_file):
        job = next(iter(challenger_service._jobs.values()))
        recorded_progress.append(job.progress)
        recorded_steps.append(job.current_step)

    with (
        patch.object(challenger_service, "_download_raw_stream", side_effect=fake_download),
        patch.object(challenger_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(challenger_service, "_write_draft_tags", side_effect=fake_tags),
    ):
        job = await challenger_service.create_job("https://www.deezer.com/track/single", "deezer")
        for _ in range(50):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.02)

    assert job.status == JobStatus.READY
    assert job.progress == 100.0
    for i in range(1, len(recorded_progress)):
        assert recorded_progress[i] >= recorded_progress[i - 1]

    # Verify step format
    assert any("Загрузка трека 1/1: Single Hit" in s for s in recorded_steps)
    assert any("Конвертация трека 1/1: Single Hit" in s for s in recorded_steps)
    assert any("Запись ID3v2.4 тегов 1/1: Single Hit" in s for s in recorded_steps)


# ---------------------------------------------------------------------------
# 6. Special Characters and Complex Titles in 12-Track Album
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_12_track_album_with_special_character_titles_step_formatting(
    challenger_service: DownloadService,
    tmp_path: Path,
):
    """Verify job.current_step correctly formats titles containing special characters,

    quotes, Cyrillic, emoji, and punctuation without crashing or mangling.
    """
    complex_titles = [
        "Розовое вино (feat. Feduk)",
        "AC/DC - Back in Black [Remastered]",
        'Title with "Double Quotes" & \'Single\'',
        "Symphony No. 5 in C Minor: I. Allegro con brio",
        "Track with emoji 🎵🔥🚀 & special symbols @#$%",
        "Very " + ("Long " * 15) + "Title for UI Truncation Test",
        "Multiple / Slashes / In / Track / Name",
        "Artist \\ Double \\ Backslash",
        "Track with 100% in title (not a progress percent)",
        "Track with leading and trailing spaces   ",
        "Специальные символы: «кавычки-ёлочки» — тире",
        "Final Track #12: The Grand Finale!",
    ]

    tracklist: list[ResolvedTrack] = [
        ResolvedTrack(
            track_id=f"spec_{idx}",
            track_number=idx,
            title=title,
            artist="Diverse Artist",
            duration=190,
            audio_source_url=f"https://youtube.com/watch?v=spec_{idx}",
        )
        for idx, title in enumerate(complex_titles, start=1)
    ]

    challenger_service._resolver.resolve = AsyncMock(
        return_value=({"album": "Diverse Album", "album_artist": "Diverse Artist"}, tracklist)
    )

    recorded_steps: list[str] = []
    orig_setattr = DownloadJob.__setattr__

    def hooked_setattr(self, name, value):
        orig_setattr(self, name, value)
        if name == "current_step":
            recorded_steps.append(value)

    async def fake_download(source_url, output_template, raw_dir, track_idx, on_progress=None):
        raw_file = raw_dir / f"{track_idx:02d}.webm"
        raw_file.write_bytes(b"stream")
        if on_progress:
            on_progress(42.0)
            on_progress(100.0)
        return raw_file

    async def fake_transcode(input_raw, output_mp3):
        output_mp3.write_bytes(b"mp3")

    def fake_tags(mp3_path, track, cover_file):
        pass

    with (
        patch.object(DownloadJob, "__setattr__", hooked_setattr),
        patch.object(challenger_service, "_download_raw_stream", side_effect=fake_download),
        patch.object(challenger_service, "_transcode_to_mp3", side_effect=fake_transcode),
        patch.object(challenger_service, "_write_draft_tags", side_effect=fake_tags),
    ):
        job = await challenger_service.create_job("https://www.deezer.com/album/diverse", "deezer")
        for _ in range(100):
            if job.status in (JobStatus.READY, JobStatus.ERROR):
                break
            await asyncio.sleep(0.02)

    assert job.status == JobStatus.READY
    assert job.progress == 100.0

    # Verify that each complex title appears verbatim in current_step
    for idx, title in enumerate(complex_titles, start=1):
        expected_dl_step = f"Загрузка трека {idx}/12: {title} (42%)"
        assert any(expected_dl_step == s for s in recorded_steps), (
            f"Missing expected step: {expected_dl_step!r}"
        )
        expected_conv_step = f"Конвертация трека {idx}/12: {title} в MP3 320k"
        assert any(expected_conv_step == s for s in recorded_steps), (
            f"Missing expected step: {expected_conv_step!r}"
        )
        expected_tag_step = f"Запись ID3v2.4 тегов {idx}/12: {title}"
        assert any(expected_tag_step == s for s in recorded_steps), (
            f"Missing expected step: {expected_tag_step!r}"
        )


# ---------------------------------------------------------------------------
# 7. Subprocess Raw Stream Output Parsing & Step Progression
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_raw_stream_stdout_streaming_integration_with_progress_and_step(
    challenger_service: DownloadService,
    tmp_path: Path,
):
    """Verify _download_raw_stream streaming parser with interleaved log lines, ANSI codes,

    and varied percentage syntax updating job state.
    """
    raw_dir = tmp_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)
    out_file = raw_dir / "01.opus"
    out_file.write_bytes(b"dummy_stream_content")

    # Interleaved stdout stream simulating real yt-dlp execution
    simulated_stdout_chunks = (
        b"[youtube] Extracting URL: https://www.youtube.com/watch?v=real_sample\n"
        b"[youtube] real_sample: Downloading webpage\n"
        b"[youtube] real_sample: Downloading ios player API JSON\n"
        b"[info] real_sample: Downloading 1 format(s): 251\n"
        b"[download] Destination: /tmp/raw/01.webm\n"
        b"  0.0%\n"
        b"  14.7%\n"
        b"[download]  25.0% of 10.00MiB at 2.00MiB/s ETA 00:03\n"
        b"download: 45.2%\n"
        b"\x1b[0;94m 70.0%\x1b[0m\n"
        b"download:\x1b[0;32m 88.5%\x1b[0m\n"
        b"100.0%\n"
        b"[download] 100% of 10.00MiB in 00:04\n"
        b"[info] Writing metadata...\n"
    )

    stdout_reader = asyncio.StreamReader()
    stdout_reader.feed_data(simulated_stdout_chunks)
    stdout_reader.feed_eof()

    stderr_reader = asyncio.StreamReader()
    stderr_reader.feed_eof()

    mock_proc = AsyncMock()
    mock_proc.returncode = 0
    mock_proc.stdout = stdout_reader
    mock_proc.stderr = stderr_reader
    mock_proc.wait = AsyncMock(return_value=0)

    received_progress: list[float] = []

    def on_prog(p: float):
        received_progress.append(p)

    with patch("asyncio.create_subprocess_exec", return_value=mock_proc):
        result_path = await challenger_service._download_raw_stream(
            source_url="https://www.youtube.com/watch?v=real_sample",
            output_template=raw_dir / "%(title)s.%(ext)s",
            raw_dir=raw_dir,
            track_idx=1,
            on_progress=on_prog,
        )

    assert result_path == out_file
    assert received_progress == [0.0, 14.7, 25.0, 45.2, 70.0, 88.5, 100.0, 100.0]

