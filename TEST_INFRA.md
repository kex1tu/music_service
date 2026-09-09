# E2E Test Infra: Music Downloader & Tag Manager

## Test Philosophy
- Opaque-box, requirement-driven. No dependency on implementation internals.
- Methodology: Category-Partition + Boundary Value Analysis (BVA) + Pairwise Combinatorial + Real-World Workload Testing.
- Zero live external network calls: LRCLIB, Genius, and Downloader interactions are strictly mocked.
- Comprehensive coverage across ID3v2.4 tags, external lyrics files, directory trees, template organization, and no-build frontend delivery.

## Feature Inventory & Test Mapping
| # | Feature | Source (requirement) | Tier 1 | Tier 2 | Tier 3 |
|---|---------|---------------------|:------:|:------:|:------:|
| 1 | ID3v2.4 mutagen tag reading | ORIGINAL_REQUEST R1 | 5 | 5 | ✓ |
| 2 | ID3v2.4 mutagen tag writing | ORIGINAL_REQUEST R1 | 5 | 5 | ✓ |
| 3 | Multi-value tag null separation | ORIGINAL_REQUEST R1 | 5 | 5 | ✓ |
| 4 | APIC front cover embedding | ORIGINAL_REQUEST R1 | 5 | 5 | ✓ |
| 5 | Two-level tag editing & batch save | ORIGINAL_REQUEST R1 | 5 | 5 | ✓ |
| 6 | LRCLIB lyrics client | ORIGINAL_REQUEST R2 | 5 | 5 | ✓ |
| 7 | Genius lyrics client fallback | ORIGINAL_REQUEST R2 | 5 | 5 | ✓ |
| 8 | External lyrics storage | ORIGINAL_REQUEST R2 | 5 | 5 | ✓ |
| 9 | Lyrics endpoints | ORIGINAL_REQUEST R2 | 5 | 5 | ✓ |
| 10 | Zero-DB directory listing | ORIGINAL_REQUEST R3 | 5 | 5 | ✓ |
| 11 | On-the-fly album tracks reading | ORIGINAL_REQUEST R3 | 5 | 5 | ✓ |
| 12 | Guarded deletion | ORIGINAL_REQUEST R3 | 5 | 5 | ✓ |
| 13 | Path sanitization | ORIGINAL_REQUEST R4 | 5 | 5 | ✓ |
| 14 | Standard path template generation | ORIGINAL_REQUEST R4 | 5 | 5 | ✓ |
| 15 | Buffer-to-library migration | ORIGINAL_REQUEST R4 | 5 | 5 | ✓ |
| 16 | Library reorganization & prune | ORIGINAL_REQUEST R4 | 5 | 5 | ✓ |
| 17 | No-build SPA layout & CDN | ORIGINAL_REQUEST R5 | 5 | 5 | ✓ |
| 18 | Alpine store & tab controllers | ORIGINAL_REQUEST R5 | 5 | 5 | ✓ |

## Test Architecture
- Test Runner: `./venv/bin/python -m pytest tests/ -v`
- Pass/Fail Semantics: 100% test pass with 0 exit code, 0 regressions against existing 101 tests.
- Linter: `./venv/bin/ruff check app tests` with 0 warnings/errors.
- Test Fixtures:
  - `fake_mp3_generator`: creates valid MPEG audio file with mutagen ID3v2.4 tags and synthetic frame headers.
  - `mock_lyrics_network`: mocks httpx requests to LRCLIB and Genius APIs.
  - `authenticated_client`: TestClient with valid session cookie.

## Real-World Application Scenarios (Tier 4)
| # | Scenario | Features Exercised | Complexity |
|---|----------|--------------------|------------|
| 1 | Various Artists Compilation | F1, F3, F4, F5, F14, F15 | High |
| 2 | Cyrillic & Special Punctuation Release | F1, F2, F13, F14, F16 | Medium |
| 3 | Offline Lyrics with Manual File Upload | F8, F9, F15, F23 | Medium |
| 4 | Loose Unorganized MP3 Adoption | F10, F11, F14, F16 | High |
| 5 | Full Download-Tag-Organize Lifecycle | F5, F8, F10, F14, F15, F16 | High |

## Coverage Thresholds
- Tier 1: >=5 per feature area
- Tier 2: >=5 per feature boundary
- Tier 3: pairwise coverage of major cross-feature flows
- Tier 4: >=5 realistic end-to-end scenarios
