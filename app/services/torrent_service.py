"""Сервис загрузки и обработки торрент-раздач (TorrentService).

Управляет фоновым процессом aria2c для скачивания музыкальных раздач
по Magnet-ссылкам и .torrent файлам. Обеспечивает:
- Потоковый парсинг прогресса aria2c (скорость, пиры, процент).
- Рекурсивный поиск аудиофайлов в скачанной раздаче.
- Парсинг CUE-листов и сплиттинг монолитных рипов через ffmpeg.
- Извлечение встроенных тегов (Vorbis/ID3/MP4) через mutagen.
- Поиск обложек (cover.jpg, folder.jpg, embedded APIC/FLAC pictures).
"""

import asyncio
import logging
import re
import shutil
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import mutagen
from mutagen.flac import FLAC
from mutagen.id3 import ID3

from app.config import Settings
from app.core.exceptions import TorrentError
from app.schemas.download import ResolvedTrack

logger = logging.getLogger("music_manager.torrent")

# Регулярное выражение для парсинга строки прогресса aria2c:
# Поддерживает фазу метаданных ([#abc123 0B/0B CN:20 SD:0 DL:0B]),
# фазу загрузки ([#abc123 45MiB/1.2GiB(3%) CN:12 SD:3 DL:5.2MiB UL:10KiB ETA:3m2s]),
# и завершение ([#abc123 100MiB/100MiB(100%) CN:0 DL:0B]).
ARIA2C_PROGRESS_REGEX = re.compile(
    r"\[#\w+\s+"
    r"(?P<dl_size>[0-9.]+[\w]+)/(?P<total_size>[0-9.]+[\w]+)"
    r"(?:\((?P<percent>\d+)%\))?"
    r"\s+CN:(?P<connections>\d+)"
    r"(?:\s+SD:\d+)?"
    r"\s+DL:(?P<speed>[0-9.]+[\w/]+)"
    r"(?:\s+UL:[0-9.]+[\w/]+)?"
    r"(?:\s+ETA:(?P<eta>[\w\d]+))?"
    r"\]"
)

# Надежные публичные BitTorrent-трекеры для анонсирования Magnet-ссылок
DEFAULT_PUBLIC_TRACKERS: str = (
    "udp://tracker.opentrackr.org:1337/announce,"
    "udp://open.tracker.cl:1337/announce,"
    "udp://9.rarbg.to:2920/announce,"
    "udp://tracker.openbittorrent.com:6969/announce,"
    "http://tracker.openbittorrent.com:80/announce,"
    "udp://opentracker.i2p.rocks:6969/announce"
)

# Точка входа для bootstrap узлов сети DHT
DEFAULT_DHT_BOOTSTRAP_NODES: str = "dht.transmissionbt.com:6881"

# Белый список аудио-расширений для фильтрации файлов раздачи
AUDIO_EXTENSIONS: frozenset[str] = frozenset({
    ".flac", ".mp3", ".m4a", ".wav", ".ape", ".wv",
    ".ogg", ".opus", ".aac",
})

# Типичные имена файлов обложек альбома (регистронезависимые)
COVER_FILENAMES: frozenset[str] = frozenset({
    "cover.jpg", "cover.png", "folder.jpg", "folder.png",
    "front.jpg", "front.png", "albumart.jpg", "albumart.png",
})


def _parse_size_to_bytes(size_str: str) -> int:
    """Преобразует строку размера aria2c (e.g. '1.2GiB', '45MiB') в байты."""
    size_str = size_str.strip()
    multipliers = {
        "kib": 1024,
        "mib": 1024 ** 2,
        "gib": 1024 ** 3,
        "tib": 1024 ** 4,
        "kb": 1000,
        "mb": 1000 ** 2,
        "gb": 1000 ** 3,
        "tb": 1000 ** 4,
        "b": 1,
    }
    lower = size_str.lower()
    for suffix, mult in sorted(multipliers.items(), key=lambda x: -len(x[0])):
        if lower.endswith(suffix):
            num_part = lower[: -len(suffix)]
            try:
                return int(float(num_part) * mult)
            except (ValueError, OverflowError):
                return 0
    try:
        return int(float(lower))
    except (ValueError, OverflowError):
        return 0


@dataclass
class CueTrack:
    """Представление одного трека из CUE-листа."""

    number: int
    title: str
    performer: str
    start_seconds: float
    end_seconds: float | None = None


class CueSheetParser:
    """Парсер CUE-листов для нарезки монолитных аудиорипов на отдельные треки."""

    @staticmethod
    def parse(cue_path: Path) -> list[CueTrack]:
        """Парсит CUE-файл и возвращает список треков с таймкодами.

        Поддерживает кодировки UTF-8 и Windows-1251 (типично для RuTracker).
        Формат таймкода INDEX 01: MM:SS:FF (75 фреймов/сек).
        """
        content = ""
        for encoding in ("utf-8-sig", "utf-8", "windows-1251", "latin-1"):
            try:
                content = cue_path.read_text(encoding=encoding)
                break
            except (UnicodeDecodeError, ValueError):
                continue

        if not content:
            logger.warning("Не удалось прочитать CUE-файл %s ни в одной кодировке", cue_path)
            return []

        tracks: list[CueTrack] = []
        current_track: CueTrack | None = None
        global_performer = ""

        for line in content.splitlines():
            line = line.strip()

            if line.upper().startswith("PERFORMER"):
                perf = line[9:].strip().strip('"')
                if current_track is not None:
                    current_track.performer = perf
                else:
                    global_performer = perf

            elif line.upper().startswith("TITLE"):
                title = line[5:].strip().strip('"')
                if current_track is not None:
                    current_track.title = title

            elif line.upper().startswith("TRACK"):
                parts = line.split()
                if len(parts) >= 2:
                    try:
                        track_num = int(parts[1])
                    except ValueError:
                        continue
                    if current_track is not None:
                        tracks.append(current_track)
                    current_track = CueTrack(
                        number=track_num,
                        title="",
                        performer=global_performer,
                        start_seconds=0.0,
                    )

            elif line.upper().startswith("INDEX 01") and current_track is not None:
                parts = line.split()
                if len(parts) >= 3:
                    time_str = parts[2]
                    time_parts = time_str.split(":")
                    if len(time_parts) == 3:
                        try:
                            mins, secs, frames = int(time_parts[0]), int(time_parts[1]), int(time_parts[2])
                            current_track.start_seconds = mins * 60 + secs + (frames / 75.0)
                        except ValueError:
                            pass

        if current_track is not None:
            tracks.append(current_track)

        # Устанавливаем end_seconds для каждого трека (кроме последнего)
        for i in range(len(tracks) - 1):
            tracks[i].end_seconds = tracks[i + 1].start_seconds

        # Заполняем пустые названия
        for track in tracks:
            if not track.title:
                track.title = f"Track {track.number:02d}"

        return tracks

    @staticmethod
    def get_referenced_files(cue_path: Path) -> list[str]:
        """Возвращает список имен файлов, указанных в директивах FILE ... WAVE/FLAC/MP3."""
        content = ""
        for encoding in ("utf-8-sig", "utf-8", "windows-1251", "latin-1"):
            try:
                content = cue_path.read_text(encoding=encoding)
                break
            except (UnicodeDecodeError, ValueError):
                continue
        if not content:
            return []

        files: list[str] = []
        for line in content.splitlines():
            line = line.strip()
            if line.upper().startswith("FILE"):
                match = re.search(r'FILE\s+"([^"]+)"', line, re.IGNORECASE)
                if match:
                    files.append(match.group(1))
                else:
                    parts = line.split()
                    if len(parts) >= 2:
                        files.append(parts[1].strip('"'))
        return files

    @staticmethod
    async def split_audio(
        audio_path: Path,
        cue_tracks: list[CueTrack],
        output_dir: Path,
    ) -> list[Path]:
        """Нарезает монолитный аудиофайл на отдельные треки по CUE-меткам через ffmpeg.

        Использует ``-c copy`` для максимальной скорости (без перекодирования).
        """
        ffmpeg_bin = shutil.which("ffmpeg") or "ffmpeg"
        output_files: list[Path] = []

        for track in cue_tracks:
            # Санитизация имени файла
            clean_title = re.sub(r'[\x00-\x1f/\\:*?"<>|%]', "_", track.title.strip())
            if not clean_title:
                clean_title = f"Track_{track.number:02d}"

            ext = audio_path.suffix  # Сохраняем оригинальное расширение (.flac, .ape, .wav)
            out_name = f"{track.number:02d}. {clean_title}{ext}"
            out_path = output_dir / out_name

            cmd: list[str] = [ffmpeg_bin, "-y"]
            if track.start_seconds > 0:
                cmd.extend(["-ss", f"{track.start_seconds:.3f}"])
            if track.end_seconds is not None:
                duration = track.end_seconds - track.start_seconds
                cmd.extend(["-t", f"{duration:.3f}"])

            cmd.extend(["-i", str(audio_path), "-c", "copy", str(out_path)])

            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.DEVNULL,
                stderr=asyncio.subprocess.PIPE,
            )
            _, stderr = await proc.communicate()
            if proc.returncode == 0 and out_path.exists():
                output_files.append(out_path)
                logger.debug("CUE сплит: трек %d → %s", track.number, out_path.name)
            else:
                err_msg = stderr.decode("utf-8", errors="replace").strip()[:200] if stderr else ""
                logger.warning(
                    "Ошибка сплиттинга трека %d из %s: rc=%d %s",
                    track.number, audio_path.name, proc.returncode, err_msg,
                )

        return output_files


def discover_audio_files(directory: Path) -> list[Path]:
    """Рекурсивно собирает аудиофайлы по белому списку расширений.

    Возвращает отсортированный список Path для стабильного порядка.
    """
    files: list[Path] = []
    if not directory.exists():
        return files
    for item in sorted(directory.rglob("*")):
        if item.is_file() and item.suffix.lower() in AUDIO_EXTENSIONS:
            files.append(item)
    return files


def extract_tags_from_file(path: Path) -> dict[str, Any]:
    """Извлекает метаданные из аудиофайла через mutagen (Vorbis/ID3/MP4 теги).

    Возвращает словарь с нормализованными ключами:
    title, artist, album, album_artist, track_number, year, genre, duration.
    """
    result: dict[str, Any] = {}
    try:
        audio = mutagen.File(path, easy=True)
        if audio is None:
            return result

        # Извлекаем длительность
        if hasattr(audio, "info") and hasattr(audio.info, "length"):
            result["duration"] = max(0, int(audio.info.length))

        # Маппинг тегов mutagen EasyID3 → нормализованные ключи
        tag_map = {
            "title": "title",
            "artist": "artist",
            "album": "album",
            "albumartist": "album_artist",
            "tracknumber": "track_number_raw",
            "date": "date",
            "genre": "genre",
        }

        for mutagen_key, our_key in tag_map.items():
            val = audio.get(mutagen_key)
            if val:
                result[our_key] = val[0]

        # Нормализация номера трека (может быть "3/12" или "03")
        raw_tn = result.pop("track_number_raw", None)
        if raw_tn:
            try:
                result["track_number"] = int(raw_tn.split("/")[0])
            except (ValueError, IndexError):
                pass

        # Извлечение года из даты (может быть "2024-03-15" или "2024")
        date_val = result.pop("date", None)
        if date_val:
            try:
                result["year"] = int(str(date_val)[:4])
            except (ValueError, IndexError):
                pass

    except Exception as exc:  # noqa: BLE001
        logger.warning("Не удалось извлечь теги из %s: %s", path.name, exc)

    return result


def find_cover_art(directory: Path, audio_files: list[Path]) -> Path | None:
    """Ищет файл обложки альбома в директории раздачи.

    Порядок приоритетов:
    1. Файлы с типичными названиями обложек (cover.jpg, folder.jpg, front.png, ...).
    2. Извлечение встроенной обложки из первого аудиофайла (FLAC pictures / ID3 APIC).
    """
    # 1. Поиск по именам файлов
    if directory.exists():
        for item in directory.rglob("*"):
            if item.is_file() and item.name.lower() in COVER_FILENAMES:
                return item

    # 2. Извлечение из аудиофайла
    if audio_files:
        first_audio = audio_files[0]
        try:
            audio = mutagen.File(first_audio)
            if audio is None:
                return None

            cover_data: bytes | None = None
            if isinstance(audio, FLAC) and audio.pictures:
                cover_data = audio.pictures[0].data
            elif hasattr(audio, "tags") and isinstance(audio.tags, ID3):
                apic_frames = audio.tags.getall("APIC")
                if apic_frames:
                    cover_data = apic_frames[0].data

            if cover_data and len(cover_data) > 100:
                cover_path = directory / "extracted_cover.jpg"
                cover_path.write_bytes(cover_data)
                return cover_path
        except Exception as exc:  # noqa: BLE001
            logger.warning("Не удалось извлечь обложку из %s: %s", first_audio.name, exc)

    return None


class TorrentService:
    """Сервис управления загрузкой и обработкой торрент-раздач через aria2c."""

    def __init__(self, settings: Settings) -> None:
        self._settings = settings

    async def download_torrent(
        self,
        magnet_or_torrent_path: str,
        output_dir: Path,
        on_progress: Callable[[int, str, str, str], None] | None = None,
    ) -> None:
        """Запускает aria2c для загрузки торрента и транслирует прогресс.

        Args:
            magnet_or_torrent_path: Magnet URI или путь к файлу .torrent.
            output_dir: Директория для сохранения файлов (внутри песочницы TMP_DIR).
            on_progress: Callback(percent, speed_str, peers_str, eta_str).

        Raises:
            TorrentError: При сбое загрузки, таймауте или превышении лимита размера.
        """
        aria2c_bin = shutil.which(self._settings.ARIA2C_PATH) or self._settings.ARIA2C_PATH
        port_start = self._settings.TORRENT_PEER_PORT
        port_end = min(port_start + 118, 65535)
        max_size_bytes = self._settings.TORRENT_MAX_SIZE_GB * 1024 ** 3

        target = magnet_or_torrent_path.strip()

        # Если передан HTTP(S) URL файла .torrent, скачиваем его во временный файл
        if target.lower().startswith("http://") or target.lower().startswith("https://"):
            import httpx

            output_dir.mkdir(parents=True, exist_ok=True)
            torrent_tmp = output_dir / "_remote.torrent"
            try:
                async with httpx.AsyncClient(follow_redirects=True, timeout=30.0) as client:
                    resp = await client.get(target)
                    resp.raise_for_status()
                    torrent_tmp.write_bytes(resp.content)
                target = str(torrent_tmp)
            except Exception as exc:
                raise TorrentError(f"Не удалось загрузить .torrent файл по ссылке: {exc}") from exc

        cmd: list[str] = [
            aria2c_bin,
            "--enable-dht=true",
            "--bt-enable-lpd=true",
            "--enable-peer-exchange=true",
            f"--dht-entry-point={DEFAULT_DHT_BOOTSTRAP_NODES}",
            f"--dht-entry-point6={DEFAULT_DHT_BOOTSTRAP_NODES}",
            f"--bt-tracker={DEFAULT_PUBLIC_TRACKERS}",
            f"--seed-time={self._settings.TORRENT_SEED_TIME}",
            "--summary-interval=1",
            f"--listen-port={port_start}-{port_end}",
            f"--bt-stop-timeout={self._settings.TORRENT_TIMEOUT}",
            "--max-overall-upload-limit=1K",
            f"--dir={output_dir}",
            "--max-connection-per-server=16",
            f"--bt-max-peers={self._settings.TORRENT_MAX_PEERS}",
            "--bt-save-metadata=false",
            "--file-allocation=none",
            "--check-integrity=false",
            "--auto-file-renaming=true",
            "--console-log-level=notice",
        ]

        if target.lower().startswith("magnet:"):
            cmd.append(target)
        else:
            cmd.extend(["--torrent-file", target])

        logger.info("Запуск aria2c для загрузки торрента в %s", output_dir)
        logger.debug("Команда aria2c: %s", " ".join(cmd))

        proc = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )

        size_exceeded = False
        stderr_lines: list[str] = []

        async def _read_stderr() -> None:
            if proc.stderr is None:
                return
            if not isinstance(proc.stderr, asyncio.StreamReader) and not asyncio.iscoroutinefunction(
                getattr(proc.stderr, "readline", None)
            ):
                return
            while True:
                line = await proc.stderr.readline()
                if not line:
                    break
                s = line.decode("utf-8", errors="replace").strip() if isinstance(line, bytes) else str(line).strip()
                if s:
                    stderr_lines.append(s)

        async def _read_stdout() -> None:
            nonlocal size_exceeded
            assert proc.stdout is not None
            while True:
                line = await proc.stdout.readline()
                if not line:
                    break
                line_str = line.decode("utf-8", errors="replace").strip()

                match = ARIA2C_PROGRESS_REGEX.search(line_str)
                if match:
                    # Проверка лимита размера раздачи
                    total_size_str = match.group("total_size")
                    total_bytes = _parse_size_to_bytes(total_size_str)
                    if total_bytes > max_size_bytes:
                        size_exceeded = True
                        logger.warning(
                            "Размер торрента %s превышает лимит %d ГБ, прерывание",
                            total_size_str, self._settings.TORRENT_MAX_SIZE_GB,
                        )
                        proc.terminate()
                        return

                    if on_progress:
                        pct_val = match.group("percent")
                        percent = int(pct_val) if pct_val is not None else 0
                        speed = match.group("speed")
                        peers = match.group("connections")
                        eta = match.group("eta") or ""
                        on_progress(percent, speed, peers, eta)

        gather_tasks = [_read_stdout(), proc.wait()]
        if proc.stderr is not None:
            gather_tasks.append(_read_stderr())

        try:
            await asyncio.wait_for(
                asyncio.gather(*gather_tasks),
                timeout=float(self._settings.TORRENT_TIMEOUT * 10),  # Общий таймаут на загрузку
            )
        except asyncio.TimeoutError:
            proc.kill()
            raise TorrentError("Превышен общий таймаут загрузки торрента")

        if size_exceeded:
            raise TorrentError(
                f"Размер раздачи превышает лимит {self._settings.TORRENT_MAX_SIZE_GB} ГБ"
            )

        if proc.returncode not in (0, None):
            tail_err = " | ".join(stderr_lines[-3:]) if stderr_lines else ""
            err_msg = f"aria2c завершился с кодом {proc.returncode}"
            if tail_err:
                err_msg += f": {tail_err}"
            raise TorrentError(err_msg)

    async def inspect_and_prepare(
        self,
        download_dir: Path,
    ) -> tuple[dict[str, Any], list[ResolvedTrack]]:
        """Инспектирует скачанную раздачу: обнаружение аудио, CUE-сплиттинг, извлечение тегов.

        Args:
            download_dir: Директория со скачанными файлами.

        Returns:
            Кортеж (album_meta, list[ResolvedTrack]) для передачи в DownloadService.
        """
        # 1. Обнаружение CUE-файлов и аудиофайлов
        cue_files = sorted(download_dir.rglob("*.cue"))
        audio_files = discover_audio_files(download_dir)

        # 2. Обработка CUE-листов (сплиттинг монолитных рипов)
        for cue_path in cue_files:
            cue_tracks = CueSheetParser.parse(cue_path)
            if not cue_tracks:
                continue

            # Находим аудиофайлы в той же директории
            cue_dir_audio = [
                f for f in audio_files
                if f.parent == cue_path.parent
            ]
            if not cue_dir_audio:
                continue

            # Если в каталоге уже несколько отдельных аудиофайлов (не меньше количества треков в CUE),
            # значит раздача уже разбита по трекам, повторная нарезка не требуется!
            if len(cue_dir_audio) > 1 and len(cue_dir_audio) >= len(cue_tracks):
                logger.info(
                    "Директория %s уже содержит %d треков (в CUE %d), повторная нарезка пропущена",
                    cue_path.parent.name, len(cue_dir_audio), len(cue_tracks),
                )
                continue

            # Проверяем, указан ли конкретный файл в CUE
            ref_files = CueSheetParser.get_referenced_files(cue_path)
            target_audio: Path | None = None
            if ref_files:
                for rf in ref_files:
                    candidate = cue_path.parent / rf
                    if candidate.exists():
                        target_audio = candidate
                        break
                    for f in cue_dir_audio:
                        if f.name.lower() == rf.lower():
                            target_audio = f
                            break

            if target_audio is None and len(cue_dir_audio) == 1:
                target_audio = cue_dir_audio[0]
            elif target_audio is None and cue_dir_audio:
                target_audio = max(cue_dir_audio, key=lambda p: p.stat().st_size)

            if target_audio is None:
                continue

            logger.info(
                "Нарезка %s по CUE-листу %s (%d треков)",
                target_audio.name, cue_path.name, len(cue_tracks),
            )

            split_files = await CueSheetParser.split_audio(
                target_audio, cue_tracks, cue_path.parent,
            )
            if split_files:
                # Заменяем монолитный файл нарезанными треками
                audio_files = [f for f in audio_files if f != target_audio]
                audio_files.extend(split_files)

        # 3. Повторная сортировка после CUE-сплиттинга
        audio_files.sort()

        if not audio_files:
            raise TorrentError("В раздаче не обнаружено аудиофайлов")

        # 4. Поиск обложки
        cover_path = find_cover_art(download_dir, audio_files)

        # 5. Извлечение тегов и формирование ResolvedTrack
        album_meta: dict[str, Any] = {
            "album": "",
            "album_artist": "",
            "year": None,
            "cover_url": None,
            "cover_path": str(cover_path) if cover_path else None,
        }

        resolved_tracks: list[ResolvedTrack] = []
        for idx, audio_path in enumerate(audio_files, start=1):
            tags = extract_tags_from_file(audio_path)

            title = tags.get("title", audio_path.stem)
            artist = tags.get("artist", "")
            album = tags.get("album", "")
            album_artist = tags.get("album_artist", "")
            year = tags.get("year")
            genre = tags.get("genre")
            track_number = tags.get("track_number", idx)
            duration = tags.get("duration", 0)

            # Обогащаем album_meta из первого трека с данными
            if not album_meta["album"] and album:
                album_meta["album"] = album
            if not album_meta["album_artist"] and (album_artist or artist):
                album_meta["album_artist"] = album_artist or artist
            if album_meta["year"] is None and year:
                album_meta["year"] = year

            resolved_tracks.append(
                ResolvedTrack(
                    track_id=f"torrent_{idx}",
                    track_number=int(track_number) if track_number else idx,
                    title=title,
                    artist=artist or album_artist or "Unknown Artist",
                    album=album,
                    album_artist=album_artist,
                    year=int(year) if year else None,
                    genre=genre,
                    duration=duration,
                    audio_source_url="",  # Не требуется — файл уже скачан
                    cover_url=None,
                    status="pending",
                )
            )

        return album_meta, resolved_tracks
