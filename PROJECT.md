# Project: Music Downloader & Tag Manager Optimization

## Architecture
- **Web Layer**: FastAPI application (`app/main.py`, `app/routers/`) exposing REST endpoints for auth, downloads, tags, lyrics, files.
- **Resolver & Matching Layer**: `SourceResolver` and `AudioMatcher` (`app/services/source_resolver.py`, `deezer_client.py`) resolving album/playlist tracks into audio sources via YouTube/Deezer.
- **Downloader Layer**: `DownloadService` (`app/services/download_service.py`) executing asynchronous `yt-dlp` and `ffmpeg` pipelines into `tmp_downloads/{job_id}/`.
- **Metadata & Library Layer**: `TagService`, `LyricsService`, `FileManagerService`, `OrganizerService` (`app/services/`) interacting with `MUSIC_DIR` strictly through `PathSandbox.resolve()`.
- **Frontend Layer**: Zero-Node.js, no-build SPA (`app/templates/index.html`, `app/static/js/`, `app/static/css/`) utilizing HTML5, Tailwind CSS CDN, Alpine.js CDN, and inline Google Material Symbols SVGs.

## Feature Inventory
| # | Feature | Description | Milestone | Source |
|---|---------|-------------|-----------|--------|
| 1 | Parallel Audio Matching | `AudioMatcher` concurrent resolution with `asyncio.Semaphore(3..5)` via `Settings.MATCHER_CONCURRENCY`, isolating per-track exceptions | M1 | ORIGINAL_REQUEST § R1 |
| 2 | yt-dlp Fast Guest Mode | Prioritize fast guest mode (`player_client=ios,visionos`) on initial attempt, bypassing YouTube antibot delays | M1 | ORIGINAL_REQUEST § R1 |
| 3 | Age Gate Cookie Fallback | Detect YouTube Age Gate errors (`Sign in to confirm your age`) and retry with cookies only when necessary | M1 | ORIGINAL_REQUEST § R1 |
| 4 | Universal yt-dlp Progress Parser | Robust regex supporting un-prefixed templates (e.g. `'  14.7%'`), stripped ANSI codes, and `--no-colors` flag | M2 | ORIGINAL_REQUEST § R2 |
| 5 | Monotonic Multi-Track Progress | Monotonic formula `job.progress = min(100.0, max(job.progress, round(10.0 + ((C + f_active) / T) * 90.0, 1)))` | M2 | ORIGINAL_REQUEST § R2 |
| 6 | Detailed Active Track UI Display | Web UI displays overall album percentage on progress bar and active track title, index, and percent with overflow protection | M2 | ORIGINAL_REQUEST § R2 |
| 7 | Google Material Icons Integration | Replace all 45 UI icons in `index.html` with official Google Material Symbols (`material-symbols`, outline/rounded) from MCP Iconify | M3 | ORIGINAL_REQUEST § R3 |
| 8 | Comprehensive Verification & Linting | Pass 100% pytest suite (all 217+ tests + new tests) and 0 errors in `ruff check app tests` | M4 | ORIGINAL_REQUEST Verification |

## Milestones
| # | Name | Scope | Dependencies | Status |
|---|------|-------|-------------|--------|
| M1 | Performance & Speed Optimization | `app/config.py`, `app/services/source_resolver.py`, `app/services/download_service.py`, `tests/test_source_resolver.py`, `tests/test_download_service.py` | none | DONE |
| M2 | Real-time Multi-Track Progress & UI Updates | `app/services/download_service.py`, `app/templates/index.html`, `app/static/js/downloader.js`, `tests/test_download_service.py` | M1 | DONE |
| M3 | Google Material Icons via MCP Iconify | `app/templates/index.html` (all 45 SVG icon replacements) | M2 | DONE |
| M4 | Comprehensive Verification & Hardening | Full test suite execution, adversarial edge case coverage, ruff lint check | M1, M2, M3 | DONE |

## Interface Contracts
### `app.config.Settings` ↔ `SourceResolver` / `AudioMatcher`
- `MATCHER_CONCURRENCY: int = 4` added to `Settings` (configurable via `.env`).
- `SourceResolver` initializes `asyncio.Semaphore(settings.MATCHER_CONCURRENCY)`.

### `SourceResolver` ↔ `DownloadService`
- `_resolve_single_track` returns `TrackInfo(..., status="ok")` on success or `TrackInfo(..., status="error", error=str(exc))` on failure.
- `DownloadService._execute_pipeline` checks `track.status != "error"` before scheduling download.

### `DownloadService` (yt-dlp) ↔ `DownloadJob` / API
- `progress_regex`: `re.compile(r"(?:\[download\]|\bdownload:)?\s*(\d+(?:\.\d+)?)%")`
- `on_track_progress`: monotonic calculation of `job.progress` and formatted `job.current_step`.

### Frontend Icons Contract
- All SVG icons in `app/templates/index.html` use `viewBox="0 0 24 24"`, `fill="currentColor"`, retaining Tailwind utility classes. Zero external build tooling.

## Code Layout
- `app/config.py`: Application settings and environment variables.
- `app/services/source_resolver.py`: Audio stream matching and URL resolution.
- `app/services/download_service.py`: Job lifecycle, yt-dlp execution, ffmpeg transcoding, and tag embedding.
- `app/templates/index.html`: Main SPA interface with Tailwind and Alpine.js.
- `app/static/js/`: Client state, API interactions, and UI reactivity.
- `tests/`: Pytest unit and integration test suite.
