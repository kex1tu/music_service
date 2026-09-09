"""Empirical stress test suite for yt-dlp progress parsing and ANSI escape code stripping.

This module provides an extensive adversarial harness testing PROGRESS_REGEX,
ANSI_ESCAPE_REGEX, and the streaming line processor used in DownloadService._download_raw_stream.
"""

from __future__ import annotations

import asyncio
import time

import pytest

from app.services.download_service import (
    ANSI_ESCAPE_REGEX,
    PROGRESS_REGEX,
    DownloadService,
)


# Helper to simulate the exact line processing in DownloadService._download_raw_stream:
#   line = line_bytes.decode("utf-8", errors="replace").strip()
#   line = re.sub(r"\x1b\[[0-9;]*[a-zA-Z]", "", line)
#   match = progress_regex.search(line)
#   if match: val = float(match.group(1)); if 0.0 <= val <= 100.0: ...
def parse_progress_line(raw_line: str | bytes) -> float | None:
    """Simulate DownloadService stdout progress extraction."""
    if isinstance(raw_line, bytes):
        line = raw_line.decode("utf-8", errors="replace").strip()
    else:
        line = raw_line.strip()

    line = ANSI_ESCAPE_REGEX.sub("", line)
    match = PROGRESS_REGEX.search(line)
    if not match:
        return None
    try:
        val = float(match.group(1))
        if 0.0 <= val <= 100.0:
            return val
        return None
    except ValueError:
        return None


# ---------------------------------------------------------------------------
# 1. Bare percentage strings (yt-dlp template: bare percent without prefix)
# ---------------------------------------------------------------------------
BARE_PERCENT_CASES = [
    ("  14.7%", 14.7),
    (" 0.0%", 0.0),
    ("0.0%", 0.0),
    ("100.0%", 100.0),
    ("100%", 100.0),
    (" 0%", 0.0),
    ("0%", 0.0),
    ("  0.1%", 0.1),
    (" 99.9%", 99.9),
    ("   50%", 50.0),
    ("50.55%", 50.55),
    ("   0.00%", 0.0),
    (" 100.00%", 100.0),
    ("   7.3%", 7.3),
    (" 85.0%", 85.0),
    (" 1.234%", 1.234),
    ("  42%", 42.0),
    (" 6.0%", 6.0),
]


@pytest.mark.parametrize(("line", "expected"), BARE_PERCENT_CASES)
def test_bare_percent_strings(line: str, expected: float) -> None:
    parsed = parse_progress_line(line)
    assert parsed is not None, f"Failed to parse bare line: {line!r}"
    assert parsed == pytest.approx(expected)


# ---------------------------------------------------------------------------
# 2. Prefixed percentage strings (`download:`, `[download]`, with varied spacing)
# ---------------------------------------------------------------------------
PREFIXED_PERCENT_CASES = [
    ("download: 45.2%", 45.2),
    ("download:  14.7%", 14.7),
    ("download:100.0%", 100.0),
    ("download: 0.0%", 0.0),
    ("download:   1.5%", 1.5),
    ("download: 99.99%", 99.99),
    ("download: 100%", 100.0),
    ("download:0%", 0.0),
    ("download:\t25.5%", 25.5),
    ("[download]  45.2%", 45.2),
    ("[download]   0.0%", 0.0),
    ("[download] 100.0%", 100.0),
    ("[download] 100%", 100.0),
    ("[download]  50.0% of 12.34MiB at 1.23MiB/s ETA 00:05", 50.0),
    ("[download]   0.0% of ~5.00MiB at 250.00KiB/s ETA 00:10", 0.0),
    ("[download] 100% of 12.34MiB in 00:10", 100.0),
    ("[download]  50.0% of 10MB", 50.0),
    ("[download] 0.5% of 100.00MiB at 5.00MiB/s ETA 00:20", 0.5),
    ("[download]  99.9% of 3.21MiB at 800.00KiB/s ETA 00:01", 99.9),
    ("[download] 100.0% of 50.00MiB in 00:04", 100.0),
    ("[download]  10.0% of ~15.20MiB at 1.50MiB/s ETA 00:08", 10.0),
]


@pytest.mark.parametrize(("line", "expected"), PREFIXED_PERCENT_CASES)
def test_prefixed_percent_strings(line: str, expected: float) -> None:
    parsed = parse_progress_line(line)
    assert parsed is not None, f"Failed to parse prefixed line: {line!r}"
    assert parsed == pytest.approx(expected)


# ---------------------------------------------------------------------------
# 3. ANSI Escape Sequences (colors, styles, cursor control)
# ---------------------------------------------------------------------------
ANSI_PERCENT_CASES = [
    ("\x1b[0;94m 14.7%\x1b[0m", 14.7),
    ("download:\x1b[0;32m 55.5%\x1b[0m", 55.5),
    ("\x1b[1;33m[download]\x1b[0m  88.8%", 88.8),
    ("\x1b[38;5;208m[download] 42.1% of 15.00MiB\x1b[0m", 42.1),
    ("\x1b[2K\x1b[1G[download]  33.3% of 10.00MiB", 33.3),
    ("\x1b[0m  77.7%\x1b[0m", 77.7),
    ("\x1b[38;2;255;100;0m 12.3%\x1b[0m", 12.3),
    ("\x1b[1m\x1b[31m[download] 99.0%\x1b[0m", 99.0),
    ("\x1b[32;1mdownload:  20.0%\x1b[0m", 20.0),
    ("\x1b[7m 100.0%\x1b[27m", 100.0),
    ("\x1b[34m[download]\x1b[0m \x1b[32m65.4%\x1b[0m of 8.00MiB", 65.4),
]


@pytest.mark.parametrize(("line", "expected"), ANSI_PERCENT_CASES)
def test_ansi_escape_percent_strings(line: str, expected: float) -> None:
    parsed = parse_progress_line(line)
    assert parsed is not None, f"Failed to parse ANSI line: {line!r}"
    assert parsed == pytest.approx(expected)


# ---------------------------------------------------------------------------
# 4. Carriage Returns `\r`, CRLF, and byte streams
# ---------------------------------------------------------------------------
CARRIAGE_RETURN_CASES = [
    ("\r  14.7%", 14.7),
    ("  14.7%\r", 14.7),
    ("\r[download]  50.0%\r", 50.0),
    ("\rdownload: 75.0%\r\n", 75.0),
    ("\r\r\r   5.0%\r\r", 5.0),
    ("\t  25.0%\t\n", 25.0),
    (b"\r  31.4%\r\n", 31.4),
    (b"\x1b[K\rdownload: 82.2%\n", 82.2),
]


@pytest.mark.parametrize(("line", "expected"), CARRIAGE_RETURN_CASES)
def test_carriage_return_strings(line: str | bytes, expected: float) -> None:
    parsed = parse_progress_line(line)
    assert parsed is not None, f"Failed to parse CR line: {line!r}"
    assert parsed == pytest.approx(expected)


# ---------------------------------------------------------------------------
# 5. Invalid lines and non-progress strings (Must be cleanly REJECTED)
# ---------------------------------------------------------------------------
INVALID_LINES = [
    "[youtube] Extracting URL",
    "[info] Writing output to...",
    "[download] Destination: 01.webm",
    "[download] Destination: /tmp/audio.webm",
    "[download] Destination: track_01.m4a",
    "  N/A%",
    "download: N/A%",
    "[download]   N/A%",
    "  Unknown%",
    "download: --.-%",
    "abc%",
    "%",
    "",
    "   ",
    "\r\n",
    "[download] at 2.50MiB/s ETA 00:01",
    "[download] ETA 00:30",
    "[download] 12.34MiB at 3.45MiB/s",
    "ERROR: [youtube] video is unavailable",
    "WARNING: [youtube] unable to extract yt-initial-data",
    "[ffmpeg] Destination: audio.mp3",
    "[Merger] Merging formats into 'audio.webm'",
    "Deleting original file audio.webm (pass -k to keep)",
    "[download] 100% of 10.00MiB at 2.00MiB/s",  # Has 100% -> this IS valid, tested below
    "105.0%",  # Out of range > 100.0
    "999%",  # Out of range > 100.0
    "-5.0%",  # Regex may match '5.0%' or negative -> let's test isolation
]

CLEAN_REJECTION_CASES = [l for l in INVALID_LINES if l != "[download] 100% of 10.00MiB at 2.00MiB/s" and l != "105.0%" and l != "999%" and l != "-5.0%"]


@pytest.mark.parametrize("line", CLEAN_REJECTION_CASES)
def test_invalid_lines_clean_rejection(line: str) -> None:
    parsed = parse_progress_line(line)
    assert parsed is None, f"Line was expected to be rejected but returned {parsed}: {line!r}"


def test_out_of_range_rejection() -> None:
    """Ensure values > 100.0 are rejected by the validation guard."""
    assert parse_progress_line("105.0%") is None
    assert parse_progress_line("999%") is None
    assert parse_progress_line("download: 100.1%") is None
    assert parse_progress_line("[download] 150% of 10MB") is None


def test_speed_lines_with_and_without_percentage() -> None:
    """Lines with speed and ETA but without '%' must reject; lines with '%' must extract accurately."""
    assert parse_progress_line("[download] at 2.50MiB/s ETA 00:01") is None
    assert parse_progress_line("[download] ETA 00:30") is None
    assert parse_progress_line("[download] 100% of 10.00MiB at 2.00MiB/s") == 100.0
    assert parse_progress_line("[download]  45.0% of 12.34MiB at 1.23MiB/s ETA 00:05") == 45.0


# ---------------------------------------------------------------------------
# 6. ReDoS / Performance & Catastrophic Backtracking Stress Tests
# ---------------------------------------------------------------------------
def test_redos_stress_resistance() -> None:
    """Verify regex execution terminates in microseconds even on adversarial inputs."""
    long_spaces = " " * 50000 + " 42.0%"
    t0 = time.perf_counter()
    m = PROGRESS_REGEX.search(long_spaces)
    elapsed = time.perf_counter() - t0
    assert m is not None and float(m.group(1)) == 42.0
    assert elapsed < 0.05, f"ReDoS vulnerability detected: {elapsed:.4f}s"

    long_non_matching = "download:" * 1000 + " 123456789" * 1000
    t0 = time.perf_counter()
    m = PROGRESS_REGEX.search(long_non_matching)
    elapsed = time.perf_counter() - t0
    assert m is None
    assert elapsed < 0.05, f"ReDoS vulnerability detected on non-match: {elapsed:.4f}s"

    long_ansi = ("\x1b[31m" * 1000) + " 88.8%" + ("\x1b[0m" * 1000)
    t0 = time.perf_counter()
    clean = ANSI_ESCAPE_REGEX.sub("", long_ansi)
    m = PROGRESS_REGEX.search(clean)
    elapsed = time.perf_counter() - t0
    assert m is not None and float(m.group(1)) == 88.8
    assert elapsed < 0.05, f"ANSI stripping ReDoS detected: {elapsed:.4f}s"


# ---------------------------------------------------------------------------
# 7. End-to-End Stream Simulation Harness
# ---------------------------------------------------------------------------
@pytest.mark.asyncio
async def test_streaming_stdout_processor_simulation() -> None:
    """Simulate exact asyncio process stdout reader loop from DownloadService._download_raw_stream."""
    feed_chunks = [
        b"[youtube] Extracting URL: https://youtube.com/watch?v=test\n",
        b"[info] Downloading formats\n",
        b"[download] Destination: /tmp/audio.webm\n",
        b"\r  0.0%\n",
        b"\r  14.7%\n",
        b"\rdownload:  25.0%\n",
        b"\x1b[0;32m 50.0%\x1b[0m\n",
        b"download: N/A%\n",  # Should be skipped
        b"\r  75.5%\n",
        b"[download] 100% of 10.00MiB in 00:05\n",
        b"[ffmpeg] Transcoding to MP3\n",
    ]

    recorded_progress: list[float] = []

    def on_progress(p: float) -> None:
        recorded_progress.append(p)

    progress_regex = DownloadService.PROGRESS_REGEX
    ansi_regex = DownloadService.ANSI_ESCAPE_REGEX

    reader = asyncio.StreamReader()
    for chunk in feed_chunks:
        reader.feed_data(chunk)
    reader.feed_eof()

    while True:
        line_bytes = await reader.readline()
        if not line_bytes:
            break
        line = line_bytes.decode("utf-8", errors="replace").strip()
        line = ansi_regex.sub("", line)
        match = progress_regex.search(line)
        if match:
            try:
                val = float(match.group(1))
                if 0.0 <= val <= 100.0:
                    on_progress(val)
            except ValueError:
                pass

    expected = [0.0, 14.7, 25.0, 50.0, 75.5, 100.0]
    assert recorded_progress == expected, f"Recorded stream progress mismatch: {recorded_progress}"
