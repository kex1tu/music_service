"""Empirical stress test suite for SourceResolver and AudioMatcher concurrency, ordering, and resilience.

Validates:
1. Concurrency limit adherence (executions strictly bounded by MATCHER_CONCURRENCY).
2. Deterministic track ordering preservation (100% matching original 1..N order under randomized delays).
3. Exception isolation and batch resilience (partial failures do not abort batch).
4. Boundary condition stress (concurrency=1, high concurrency, all-fail vs 1-survivor).
"""

import asyncio
import random
import time
from unittest.mock import AsyncMock, MagicMock

import pytest

from app.config import Settings
from app.core.exceptions import AudioMatchError
from app.services.deezer_client import DeezerClient
from app.services.source_resolver import AudioMatcher, SourceResolver


@pytest.mark.asyncio
async def test_source_resolver_concurrency_and_ordering_stress_25_tracks():
    """Empirically stress-test SourceResolver with 25 tracks and randomized delays (0.01s - 0.20s).

    Verifications:
    - Concurrent active executions never exceed MATCHER_CONCURRENCY.
    - Concurrency is actively utilized (max_concurrent reaches MATCHER_CONCURRENCY).
    - Track ordering in the returned list matches 100% with original track numbers (1..25).
    - Wall-clock time demonstrates genuine concurrent execution versus sequential sum.
    """
    total_tracks = 25
    concurrency_limit = 4
    rng = random.Random(42)
    # Generate randomized completion delays between 0.01s and 0.20s
    delays = {i: rng.uniform(0.01, 0.20) for i in range(1, total_tracks + 1)}
    sum_delays = sum(delays.values())

    raw_tracks = [
        {
            "id": 1000 + i,
            "title": f"Stress Track {i:02d}",
            "duration": 150 + i,
            "track_position": i,
            "contributors": [{"name": f"Artist {i}"}],
        }
        for i in range(1, total_tracks + 1)
    ]

    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("album", "123456"))
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 123456,
            "title": "Stress Test Album",
            "release_date": "2024-05-10",
            "tracks": {"data": raw_tracks},
        }
    )

    active_count = 0
    max_active = 0
    lock = asyncio.Lock()
    execution_history: list[int] = []

    async def stress_find_best_match(title: str, **kwargs):
        nonlocal active_count, max_active
        track_num = int(title.split()[-1])

        async with lock:
            active_count += 1
            max_active = max(max_active, active_count)
            assert active_count <= concurrency_limit, (
                f"Concurrency violation: active_count={active_count} exceeded limit={concurrency_limit}"
            )

        # Simulate randomized I/O delay
        delay = delays[track_num]
        await asyncio.sleep(delay)

        async with lock:
            active_count -= 1
            execution_history.append(track_num)

        return f"https://youtube.com/watch?v=stress_track_{track_num:02d}"

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(side_effect=stress_find_best_match)

    settings = Settings(MATCHER_CONCURRENCY=concurrency_limit)
    resolver = SourceResolver(
        deezer_client=mock_deezer,
        audio_matcher=mock_matcher,
        settings=settings,
    )

    start_time = time.monotonic()
    _meta, tracks = await resolver.resolve("https://www.deezer.com/album/123456")
    elapsed_time = time.monotonic() - start_time

    # 1. Concurrency verification
    assert max_active == concurrency_limit, (
        f"Expected max concurrent tasks to reach {concurrency_limit}, got {max_active}"
    )
    assert active_count == 0, f"Expected 0 active tasks at end, got {active_count}"

    # 2. Ordering verification
    assert len(tracks) == total_tracks
    for idx, track in enumerate(tracks, start=1):
        assert track.track_number == idx, f"Track at index {idx-1} has wrong track_number {track.track_number}"
        assert track.title == f"Stress Track {idx:02d}"
        assert track.audio_source_url == f"https://youtube.com/watch?v=stress_track_{idx:02d}"
        assert track.status == "pending"
        assert track.error is None

    # 3. Execution completion was out of order (proving race conditions were tested)
    original_order = list(range(1, total_tracks + 1))
    assert execution_history != original_order, (
        "Completion order unexpectedly matched input order; race condition not exercised"
    )

    # 4. Performance speedup verification: elapsed time should be substantially less than sequential sum
    assert elapsed_time < sum_delays * 0.5, (
        f"Parallel resolution ({elapsed_time:.2f}s) was not significantly faster than sequential ({sum_delays:.2f}s)"
    )


@pytest.mark.asyncio
async def test_source_resolver_partial_failure_with_5_exceptions_out_of_20_tracks():
    """Empirically stress-test partial failure handling: 5 out of 20 tracks throw varied exceptions.

    Verifications:
    - 5 failing tracks do NOT abort the batch.
    - No unhandled exception is raised to the caller.
    - Exactly 20 tracks are returned in the exact 1..20 sequence.
    - Failing tracks are marked status='error' with empty audio_url and error description.
    - The remaining 15 tracks are marked status='pending' with valid audio_url and error=None.
    """
    total_tracks = 20
    concurrency_limit = 4
    failing_tracks = {
        3: AudioMatchError("No stream candidate found"),
        7: RuntimeError("Upstream network socket timeout"),
        11: ValueError("Malformed candidate duration response"),
        15: TimeoutError("YouTube search query timed out"),
        19: ConnectionResetError("Connection reset by peer during matching"),
    }

    rng = random.Random(1337)
    delays = {i: rng.uniform(0.01, 0.15) for i in range(1, total_tracks + 1)}

    raw_tracks = [
        {
            "id": 2000 + i,
            "title": f"Batch Track {i:02d}",
            "duration": 180,
            "track_position": i,
            "contributors": [{"name": "Batch Artist"}],
        }
        for i in range(1, total_tracks + 1)
    ]

    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("album", "654321"))
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 654321,
            "title": "Partial Failure Album",
            "release_date": "2024-01-01",
            "tracks": {"data": raw_tracks},
        }
    )

    active_count = 0
    max_active = 0
    lock = asyncio.Lock()

    async def flaky_find_best_match(title: str, **kwargs):
        nonlocal active_count, max_active
        track_num = int(title.split()[-1])

        async with lock:
            active_count += 1
            max_active = max(max_active, active_count)
            assert active_count <= concurrency_limit

        await asyncio.sleep(delays[track_num])

        async with lock:
            active_count -= 1

        if track_num in failing_tracks:
            raise failing_tracks[track_num]

        return f"https://youtube.com/watch?v=valid_track_{track_num:02d}"

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(side_effect=flaky_find_best_match)

    settings = Settings(MATCHER_CONCURRENCY=concurrency_limit)
    resolver = SourceResolver(
        deezer_client=mock_deezer,
        audio_matcher=mock_matcher,
        settings=settings,
    )

    # Resolution must NOT raise an exception
    _meta, tracks = await resolver.resolve("https://www.deezer.com/album/654321")

    assert len(tracks) == 20
    assert max_active <= concurrency_limit

    # Verify track ordering and per-track status
    for idx, track in enumerate(tracks, start=1):
        assert track.track_number == idx
        assert track.title == f"Batch Track {idx:02d}"

        if idx in failing_tracks:
            expected_err = str(failing_tracks[idx])
            assert track.status == "error", f"Track {idx} was expected to have status='error', got '{track.status}'"
            assert track.audio_source_url == ""
            assert track.error is not None
            assert expected_err in track.error
        else:
            assert track.status == "pending", f"Track {idx} was expected to have status='pending', got '{track.status}'"
            assert track.audio_source_url == f"https://youtube.com/watch?v=valid_track_{idx:02d}"
            assert track.error is None

    # Count ok vs error
    error_count = sum(1 for t in tracks if t.status == "error")
    ok_count = sum(1 for t in tracks if t.status == "pending")
    assert error_count == 5
    assert ok_count == 15


@pytest.mark.asyncio
@pytest.mark.parametrize("concurrency", [1, 3, 5, 8])
async def test_source_resolver_varying_concurrency_boundaries(concurrency: int):
    """Empirically test different concurrency limits (1, 3, 5, 8) with 20 tracks."""
    total_tracks = 20
    rng = random.Random(concurrency * 100)
    delays = {i: rng.uniform(0.005, 0.03) for i in range(1, total_tracks + 1)}

    raw_tracks = [
        {
            "id": 3000 + i,
            "title": f"Param Track {i:02d}",
            "duration": 200,
            "track_position": i,
            "contributors": [{"name": "Param Artist"}],
        }
        for i in range(1, total_tracks + 1)
    ]

    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("album", f"param_{concurrency}"))
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 999,
            "title": "Param Album",
            "release_date": "2024-01-01",
            "tracks": {"data": raw_tracks},
        }
    )

    active_count = 0
    max_active = 0
    lock = asyncio.Lock()

    async def param_find_best_match(title: str, **kwargs):
        nonlocal active_count, max_active
        track_num = int(title.split()[-1])

        async with lock:
            active_count += 1
            max_active = max(max_active, active_count)
            assert active_count <= concurrency, f"Exceeded concurrency limit {concurrency}"

        await asyncio.sleep(delays[track_num])

        async with lock:
            active_count -= 1

        return f"https://youtube.com/watch?v=param_{track_num}"

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(side_effect=param_find_best_match)

    settings = Settings(MATCHER_CONCURRENCY=concurrency)
    resolver = SourceResolver(
        deezer_client=mock_deezer,
        audio_matcher=mock_matcher,
        settings=settings,
    )

    _, tracks = await resolver.resolve(f"https://www.deezer.com/album/param_{concurrency}")

    assert len(tracks) == total_tracks
    assert max_active <= concurrency
    assert max_active == min(concurrency, total_tracks)
    assert [t.track_number for t in tracks] == list(range(1, total_tracks + 1))


@pytest.mark.asyncio
async def test_source_resolver_all_20_tracks_fail_raises_audiomatcherror():
    """Empirically test that when 100% of tracks fail matching, AudioMatchError is raised."""
    total_tracks = 20
    raw_tracks = [
        {
            "id": 4000 + i,
            "title": f"Fail Track {i:02d}",
            "duration": 180,
            "track_position": i,
            "contributors": [{"name": "Fail Artist"}],
        }
        for i in range(1, total_tracks + 1)
    ]

    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("album", "all_fail"))
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 404,
            "title": "All Fail Album",
            "release_date": "2024-01-01",
            "tracks": {"data": raw_tracks},
        }
    )

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(side_effect=AudioMatchError("Simulated total failure"))

    settings = Settings(MATCHER_CONCURRENCY=4)
    resolver = SourceResolver(
        deezer_client=mock_deezer,
        audio_matcher=mock_matcher,
        settings=settings,
    )

    with pytest.raises(AudioMatchError, match="Не удалось сопоставить ни один трек из альбома"):
        await resolver.resolve("https://www.deezer.com/album/all_fail")


@pytest.mark.asyncio
async def test_source_resolver_19_of_20_fail_survives():
    """Empirically test that when 19 of 20 tracks fail, the 1 survivor prevents album abort."""
    total_tracks = 20
    raw_tracks = [
        {
            "id": 5000 + i,
            "title": f"Survivor Track {i:02d}",
            "duration": 180,
            "track_position": i,
            "contributors": [{"name": "Survivor Artist"}],
        }
        for i in range(1, total_tracks + 1)
    ]

    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("album", "survivor"))
    mock_deezer.get_album = AsyncMock(
        return_value={
            "id": 505,
            "title": "Survivor Album",
            "release_date": "2024-01-01",
            "tracks": {"data": raw_tracks},
        }
    )

    async def single_survivor_match(title: str, **kwargs):
        track_num = int(title.split()[-1])
        if track_num == 14:
            return "https://youtube.com/watch?v=lucky_survivor_14"
        raise AudioMatchError(f"Track {track_num} failed")

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(side_effect=single_survivor_match)

    settings = Settings(MATCHER_CONCURRENCY=4)
    resolver = SourceResolver(
        deezer_client=mock_deezer,
        audio_matcher=mock_matcher,
        settings=settings,
    )

    _, tracks = await resolver.resolve("https://www.deezer.com/album/survivor")

    assert len(tracks) == 20
    assert [t.track_number for t in tracks] == list(range(1, 21))

    error_tracks = [t for t in tracks if t.status == "error"]
    ok_tracks = [t for t in tracks if t.status == "pending"]

    assert len(error_tracks) == 19
    assert len(ok_tracks) == 1
    assert ok_tracks[0].track_number == 14
    assert ok_tracks[0].audio_source_url == "https://youtube.com/watch?v=lucky_survivor_14"


@pytest.mark.asyncio
async def test_source_resolver_deezer_playlist_concurrency_and_ordering():
    """Empirically test Deezer playlist resolving (res_type='playlist') with 20 tracks and randomized delays."""
    total_tracks = 22
    concurrency_limit = 4
    rng = random.Random(777)
    delays = {i: rng.uniform(0.01, 0.10) for i in range(1, total_tracks + 1)}

    raw_tracks = [
        {
            "id": 6000 + i,
            "title": f"Playlist Track {i:02d}",
            "duration": 190,
            "track_position": i,
            "contributors": [{"name": f"Playlist Artist {i}"}],
        }
        for i in range(1, total_tracks + 1)
    ]

    mock_deezer = MagicMock(spec=DeezerClient)
    mock_deezer.parse_url = AsyncMock(return_value=("playlist", "playlist_999"))
    mock_deezer.get_playlist = AsyncMock(
        return_value={
            "id": 999,
            "title": "Stress Playlist",
            "tracks": {"data": raw_tracks},
        }
    )

    active_count = 0
    max_active = 0
    lock = asyncio.Lock()

    async def playlist_match(title: str, **kwargs):
        nonlocal active_count, max_active
        track_num = int(title.split()[-1])

        async with lock:
            active_count += 1
            max_active = max(max_active, active_count)
            assert active_count <= concurrency_limit

        await asyncio.sleep(delays[track_num])

        async with lock:
            active_count -= 1

        return f"https://youtube.com/watch?v=pl_{track_num:02d}"

    mock_matcher = MagicMock(spec=AudioMatcher)
    mock_matcher.find_best_match = AsyncMock(side_effect=playlist_match)

    settings = Settings(MATCHER_CONCURRENCY=concurrency_limit)
    resolver = SourceResolver(
        deezer_client=mock_deezer,
        audio_matcher=mock_matcher,
        settings=settings,
    )

    meta, tracks = await resolver.resolve("https://www.deezer.com/playlist/playlist_999")

    assert meta["album"] == "Stress Playlist"
    assert len(tracks) == total_tracks
    assert max_active == concurrency_limit
    assert [t.track_number for t in tracks] == list(range(1, total_tracks + 1))
    assert all(t.status == "pending" for t in tracks)
    assert all(t.audio_source_url.startswith("https://youtube.com/watch?v=pl_") for t in tracks)
