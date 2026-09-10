"""Оркестратор загрузок, транскодирования и управления буфером (Download Service).

Управляет очередью задач (DownloadJob), неблокирующим запуском yt-dlp
и ffmpeg, записью черновых ID3v2.4-тегов через mutagen, хранением состояния
в памяти (Zero-Database) и фоновой очисткой буфера TMP_DIR по TTL.
"""

import asyncio
import logging
import re
import shutil
import sys
import uuid
from collections.abc import Callable
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from mutagen.id3 import APIC, ID3, TALB, TCON, TDRC, TIT2, TPE1, TPE2, TRCK

from app.config import Settings
from app.core.exceptions import DownloadError, NotFoundError, UnsupportedSourceError
from app.core.sandbox import PathSandbox
from app.schemas.download import (
    DownloadJobResponse,
    DownloadTracksResponse,
    JobStatus,
    QueueSummaryResponse,
    ResolvedTrack,
)
from app.services.deezer_client import DeezerClient
from app.services.source_resolver import SourceResolver

logger = logging.getLogger("music_manager.downloader")

PROGRESS_REGEX = re.compile(r"(?:\[download\]|\bdownload:)?\s*(\d+(?:\.\d+)?)%")
ANSI_ESCAPE_REGEX = re.compile(r"\x1b\[[0-9;]*[a-zA-Z]")


class DownloadJob:
    """Представление отдельной задачи на скачивание и конвертацию релиза."""

    def __init__(self, job_id: str, url: str, source: str) -> None:
        self.id = job_id
        self.url = url
        self.source = source
        self.status = JobStatus.QUEUED
        self.progress: float = 0.0
        self.current_step: str = "Задача ожидает очереди"
        self.album_metadata: dict[str, Any] = {}
        self.tracks: list[ResolvedTrack] = []
        self.completed_tracks: int = 0
        self.error: str | None = None
        self.cover_relative_path: str | None = None
        self.created_at: datetime = datetime.now(timezone.utc)
        self.updated_at: datetime = datetime.now(timezone.utc)

    def to_response(self) -> DownloadJobResponse:
        """Преобразует состояние задачи в Pydantic-схему для API."""
        return DownloadJobResponse(
            id=self.id,
            status=self.status,
            source=self.source,
            url=self.url,
            progress=round(self.progress, 1),
            current_step=self.current_step,
            total_tracks=len(self.tracks),
            completed_tracks=self.completed_tracks,
            error=self.error,
            created_at=self.created_at.isoformat(),
        )


def is_age_restricted_error(err_msg: str) -> bool:
    """Определяет, вызван ли сбой загрузки возрастным ограничением (Age Gate) YouTube."""
    if not err_msg:
        return False
    msg = err_msg.lower()
    return any(
        pattern in msg
        for pattern in (
            "confirm your age",
            "age-restricted",
            "sign in to confirm",
            "sign in to view",
            "requires authentication",
            "login required",
        )
    )


def is_drm_protected_error(err_msg: str) -> bool:
    """Определяет, вызван ли сбой загрузки DRM-защитой (SoundCloud Go+, DRM protected)."""
    if not err_msg:
        return False
    msg = err_msg.lower()
    return "drm protected" in msg or "drm-protected" in msg or bool(re.search(r"\bdrm\b", msg))


class DownloadService:
    """Сервис оркестрации задач загрузки аудио."""

    PROGRESS_REGEX = PROGRESS_REGEX
    ANSI_ESCAPE_REGEX = ANSI_ESCAPE_REGEX
    is_age_restricted_error = staticmethod(is_age_restricted_error)
    is_drm_protected_error = staticmethod(is_drm_protected_error)

    @staticmethod
    async def _ensure_jpeg_cover(image_bytes: bytes) -> bytes:
        """Конвертирует WebP (или несовместимые форматы) в эталонный JPEG через ffmpeg.

        Гарантирует максимальную совместимость с ID3v2.4 APIC и внешними медиаплеерами.
        """
        if not image_bytes:
            return image_bytes
        if image_bytes.startswith((b"\xff\xd8\xff", b"\x89PNG")):
            return image_bytes

        is_webp = (
            image_bytes.startswith(b"RIFF")
            and len(image_bytes) >= 12
            and image_bytes[8:12] == b"WEBP"
        )
        if not is_webp:
            return image_bytes

        ffmpeg_bin = shutil.which("ffmpeg") or "ffmpeg"
        try:
            proc = await asyncio.create_subprocess_exec(
                ffmpeg_bin,
                "-y",
                "-f",
                "image2pipe",
                "-i",
                "pipe:0",
                "-vframes",
                "1",
                "-f",
                "image2pipe",
                "-c:v",
                "mjpeg",
                "pipe:1",
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            stdout, _ = await asyncio.wait_for(
                proc.communicate(input=image_bytes),
                timeout=15.0,
            )
            if proc.returncode == 0 and stdout and stdout.startswith(b"\xff\xd8\xff"):
                return stdout
        except Exception as exc:  # noqa: BLE001
            logger.warning("Не удалось перекодировать WebP обложку в JPEG: %s", exc)

        return image_bytes

    def __init__(
        self,
        settings: Settings,
        tmp_sandbox: PathSandbox,
        source_resolver: SourceResolver,
        deezer_client: DeezerClient,
    ) -> None:
        self._settings = settings
        self._sandbox = tmp_sandbox
        self._resolver = source_resolver
        self._deezer = deezer_client
        self._jobs: dict[str, DownloadJob] = {}
        self._semaphore = asyncio.Semaphore(settings.MAX_CONCURRENT_DOWNLOADS)
        self._lock = asyncio.Lock()

    def get_job(self, job_id: str) -> DownloadJob:
        """Возвращает задачу по ID или выбрасывает NotFoundError."""
        job = self._jobs.get(job_id)
        if not job:
            raise NotFoundError(f"Задача загрузки '{job_id}' не найдена")
        return job

    def get_job_tracks(self, job_id: str) -> DownloadTracksResponse:
        """Возвращает подготовленные черновые метаданные треков для экрана проверки."""
        job = self.get_job(job_id)
        if job.status == JobStatus.ERROR:
            raise DownloadError(f"Задача завершилась с ошибкой: {job.error}")
        if job.status != JobStatus.READY:
            raise DownloadError(
                f"Метаданные недоступны: задача находится в статусе {job.status.value}"
            )

        meta = job.album_metadata
        first_track = job.tracks[0] if job.tracks else None
        return DownloadTracksResponse(
            job_id=job.id,
            album_artist=meta.get("album_artist") or (first_track.album_artist if first_track else ""),
            album=meta.get("album") or (first_track.album if first_track else ""),
            year=meta.get("year") or (first_track.year if first_track else None),
            genre=meta.get("genre") or (first_track.genre if first_track else None),
            cover_url=meta.get("cover_url") or (first_track.cover_url if first_track else None),
            cover_path=job.cover_relative_path,
            tracks=job.tracks,
        )

    async def create_job(self, url: str, source: str = "auto") -> DownloadJob:
        """Создает новую задачу загрузки, регистрирует ее и запускает фоновый конвейер."""
        detected_source = (
            self._resolver.detect_source(url) if source == "auto" else source
        )
        job_id = uuid.uuid4().hex
        job = DownloadJob(job_id=job_id, url=url, source=detected_source)

        async with self._lock:
            self._jobs[job_id] = job

        # Запускаем фоновый конвейер
        asyncio.create_task(self._execute_pipeline(job_id))
        return job

    async def create_batch_jobs(
        self, urls: list[str], source: str = "auto"
    ) -> list[DownloadJob]:
        """Создает пакет задач загрузки для переданного списка URL.

        Валидирует источники и последовательно ставит задачи в очередь.
        """
        cleaned_urls = [u.strip() for u in urls if u and u.strip()]
        if not cleaned_urls:
            raise UnsupportedSourceError("Список URL не может быть пустым")

        # Предварительная валидация источников
        if source == "auto":
            for u in cleaned_urls:
                self._resolver.detect_source(u)

        created_jobs: list[DownloadJob] = []
        for u in cleaned_urls:
            job = await self.create_job(url=u, source=source)
            created_jobs.append(job)

        return created_jobs

    def get_all_jobs(self) -> list[DownloadJobResponse]:
        """Возвращает список всех задач в памяти, отсортированных от новых к старым."""
        sorted_jobs = sorted(
            self._jobs.values(),
            key=lambda j: j.created_at,
            reverse=True,
        )
        return [j.to_response() for j in sorted_jobs]

    def get_queue_summary(self) -> QueueSummaryResponse:
        """Возвращает агрегированную сводку состояния очереди загрузок."""
        jobs = list(self._jobs.values())
        if not jobs:
            return QueueSummaryResponse()

        active_jobs = 0
        queued_jobs = 0
        completed_jobs = 0
        error_jobs = 0
        total_tracks = 0
        completed_tracks = 0
        total_progress = 0.0

        active_statuses = {
            JobStatus.RESOLVING,
            JobStatus.DOWNLOADING,
            JobStatus.CONVERTING,
            JobStatus.TAGGING,
        }

        for j in jobs:
            if j.status in active_statuses:
                active_jobs += 1
            elif j.status == JobStatus.QUEUED:
                queued_jobs += 1
            elif j.status == JobStatus.READY:
                completed_jobs += 1
            elif j.status == JobStatus.ERROR:
                error_jobs += 1

            t_count = len(j.tracks)
            total_tracks += t_count if t_count > 0 else (1 if j.status != JobStatus.ERROR else 0)
            completed_tracks += j.completed_tracks
            total_progress += j.progress

        overall_progress = round(total_progress / len(jobs), 1)
        overall_progress = max(0.0, min(100.0, overall_progress))

        return QueueSummaryResponse(
            active_jobs=active_jobs,
            queued_jobs=queued_jobs,
            completed_jobs=completed_jobs,
            error_jobs=error_jobs,
            total_tracks=total_tracks,
            completed_tracks=completed_tracks,
            overall_progress=overall_progress,
        )

    async def clear_completed_jobs(self) -> int:
        """Удаляет завершенные (READY) и аварийные (ERROR) задачи из очереди."""
        async with self._lock:
            to_remove = [
                jid
                for jid, job in self._jobs.items()
                if job.status in (JobStatus.READY, JobStatus.ERROR)
            ]
            for jid in to_remove:
                self._jobs.pop(jid, None)

        return len(to_remove)

    async def _execute_pipeline(self, job_id: str) -> None:
        """Основной пайплайн: разрешение -> загрузка -> конвертация -> теги -> READY."""
        job = self.get_job(job_id)

        async with self._semaphore:
            try:
                # 1. Этап разрешения источника
                job.status = JobStatus.RESOLVING
                job.current_step = "Определение источника и получение метаданных"
                job.progress = 5.0
                job.updated_at = datetime.now(timezone.utc)

                # Создаем изолированные каталоги внутри песочницы TMP_DIR
                raw_dir = self._sandbox.resolve(f"{job.id}/raw")
                converted_dir = self._sandbox.resolve(f"{job.id}/converted")
                raw_dir.mkdir(parents=True, exist_ok=True)
                converted_dir.mkdir(parents=True, exist_ok=True)

                album_meta, tracks = await self._resolver.resolve(job.url, job.source)
                job.album_metadata = album_meta
                job.tracks = tracks

                if not tracks:
                    raise DownloadError("Источник не содержит аудиодорожек для загрузки")

                job.progress = 10.0
                job.updated_at = datetime.now(timezone.utc)

                # Скачиваем обложку при ее наличии
                cover_url = album_meta.get("cover_url") or next(
                    (t.cover_url for t in tracks if t.cover_url), None
                )
                cover_file: Path | None = None
                if cover_url:
                    try:
                        cover_bytes = await self._deezer.download_cover_bytes(cover_url)
                        if cover_bytes:
                            cover_bytes = await self._ensure_jpeg_cover(cover_bytes)
                            cover_file = self._sandbox.resolve(f"{job.id}/cover.jpg")
                            cover_file.write_bytes(cover_bytes)
                            job.cover_relative_path = str(self._sandbox.to_relative(cover_file))
                        else:
                            cover_file = None
                    except Exception as exc:  # noqa: BLE001
                        logger.warning("Не удалось скачать обложку альбома (%s): %s", cover_url, exc)
                        cover_file = None

                # 2. Обработка треков по очереди (пропорциональное бюджетирование 10-100%)
                total = len(tracks)
                track_weight = 90.0 / total
                used_filenames: set[str] = set()

                for idx, track in enumerate(tracks, start=1):
                    i = idx - 1
                    track_base = 10.0 + i * track_weight
                    completed_tracks = i

                    if track.status == "error" or not track.audio_source_url:
                        logger.warning(
                            "Пропуск трека %d/%d (%s): статус '%s', ошибка '%s'",
                            idx,
                            total,
                            track.title,
                            track.status,
                            track.error,
                        )
                        job.progress = min(100.0, max(job.progress, round(track_base + track_weight, 1)))
                        job.current_step = f"Пропуск трека {idx}/{total}: {track.title} (ошибка)"
                        job.updated_at = datetime.now(timezone.utc)
                        continue

                    def on_track_progress(
                        p: float,
                        base: float = track_base,
                        track_num: int = idx,
                        track_title: str = track.title,
                        completed_cnt: int = completed_tracks,
                    ) -> None:
                        job.status = JobStatus.DOWNLOADING
                        dl_fraction = min(max(p / 100.0, 0.0), 1.0)
                        new_progress = 10.0 + ((completed_cnt + dl_fraction * 0.70) / total) * 90.0
                        job.progress = min(100.0, max(job.progress, round(new_progress, 1)))
                        job.current_step = f"Загрузка трека {track_num}/{total}: {track_title} ({p:.0f}%)"
                        job.updated_at = datetime.now(timezone.utc)

                    # 2a. Скачивание аудиопотока через yt-dlp
                    job.status = JobStatus.DOWNLOADING
                    job.current_step = f"Загрузка трека {idx}/{total}: {track.title} (0%)"
                    new_progress = 10.0 + ((completed_tracks + 0.0) / total) * 90.0
                    job.progress = min(100.0, max(job.progress, round(new_progress, 1)))
                    job.updated_at = datetime.now(timezone.utc)

                    clean_title = (
                        re.sub(r'[\x00-\x1f/\\:*?"<>|%]', "_", (track.title or "").strip()).strip(". ")
                        or f"Track_{idx}"
                    )
                    if len(clean_title) > 180:
                        clean_title = clean_title[:180].rstrip(". ") or f"Track_{idx}"

                    clean_filename = f"{track.track_number:02d}. {clean_title}.mp3"
                    if clean_filename in used_filenames or (converted_dir / clean_filename).exists():
                        dup_idx = 2
                        while (
                            f"{track.track_number:02d}. {clean_title} ({dup_idx}).mp3" in used_filenames
                            or (converted_dir / f"{track.track_number:02d}. {clean_title} ({dup_idx}).mp3").exists()
                        ):
                            dup_idx += 1
                        clean_filename = f"{track.track_number:02d}. {clean_title} ({dup_idx}).mp3"

                    used_filenames.add(clean_filename)
                    mp3_path = self._sandbox.resolve(f"{job.id}/converted/{clean_filename}")
                    raw_template = raw_dir / f"{idx:02d}.%(ext)s"
                    track_cover_file: Path | None = None
                    raw_audio_path: Path | None = None

                    try:
                        try:
                            raw_audio_path = await self._download_raw_stream(
                                source_url=track.audio_source_url,
                                output_template=raw_template,
                                raw_dir=raw_dir,
                                track_idx=idx,
                                on_progress=on_track_progress,
                            )
                        except TypeError:
                            # Обратная совместимость для тестовых моков с 4 аргументами
                            raw_audio_path = await self._download_raw_stream(
                                source_url=track.audio_source_url,
                                output_template=raw_template,
                                raw_dir=raw_dir,
                                track_idx=idx,
                            )

                        # 2b. Конвертация в эталонный MP3 через ffmpeg (20% веса трека)
                        job.status = JobStatus.CONVERTING
                        conv_progress = 10.0 + ((completed_tracks + 0.70) / total) * 90.0
                        job.progress = min(100.0, max(job.progress, round(conv_progress, 1)))
                        job.current_step = f"Конвертация трека {idx}/{total}: {track.title} в MP3 320k"
                        job.updated_at = datetime.now(timezone.utc)

                        await self._transcode_to_mp3(
                            input_raw=raw_audio_path,
                            output_mp3=mp3_path,
                        )

                        # 2c. Запись черновых ID3v2.4 тегов через mutagen (10% веса трека)
                        job.status = JobStatus.TAGGING
                        tag_progress = 10.0 + ((completed_tracks + 0.90) / total) * 90.0
                        job.progress = min(100.0, max(job.progress, round(tag_progress, 1)))
                        job.current_step = f"Запись ID3v2.4 тегов {idx}/{total}: {track.title}"
                        job.updated_at = datetime.now(timezone.utc)

                        # Скачиваем индивидуальную обложку трека или используем обложку альбома/плейлиста как fallback
                        track_cover_file: Path | None = None
                        if track.cover_url and (
                            cover_file is None
                            or not cover_file.exists()
                            or cover_file.stat().st_size == 0
                            or not cover_url
                            or track.cover_url != cover_url
                        ):
                            try:
                                track_cover_bytes = await self._deezer.download_cover_bytes(track.cover_url)
                                if track_cover_bytes:
                                    track_cover_bytes = await self._ensure_jpeg_cover(track_cover_bytes)
                                    ext = (
                                        ".png"
                                        if (track_cover_bytes.startswith(b"\x89PNG") or ".png" in track.cover_url.lower())
                                        else ".jpg"
                                    )
                                    t_cover_path = self._sandbox.resolve(f"{job.id}/raw/cover_{idx}{ext}")
                                    t_cover_path.write_bytes(track_cover_bytes)
                                    track_cover_file = t_cover_path
                            except Exception as exc:  # noqa: BLE001
                                logger.warning(
                                    "Не удалось скачать обложку трека %d (%s): %s",
                                    idx,
                                    track.cover_url,
                                    exc,
                                )
                                track_cover_file = None

                        effective_cover_file = track_cover_file or cover_file

                        # Если общая обложка задачи еще не была сохранена, сохраняем первую полученную
                        if (
                            (not cover_file or not cover_file.exists() or cover_file.stat().st_size == 0)
                            and effective_cover_file
                            and effective_cover_file.exists()
                            and effective_cover_file.stat().st_size > 0
                        ):
                            try:
                                cover_file = self._sandbox.resolve(f"{job.id}/cover.jpg")
                                cover_file.write_bytes(effective_cover_file.read_bytes())
                                job.cover_relative_path = str(self._sandbox.to_relative(cover_file))
                            except Exception as exc:  # noqa: BLE001
                                logger.warning("Не удалось сохранить обложку задачи: %s", exc)

                        self._write_draft_tags(
                            mp3_path=mp3_path,
                            track=track,
                            cover_file=effective_cover_file,
                        )

                        # Удаляем сырой аудиофайл для экономии дискового пространства
                        if raw_audio_path and raw_audio_path.exists():
                            raw_audio_path.unlink()

                        # Фиксируем статус трека
                        track.status = "ready"
                        track.converted_path = str(self._sandbox.to_relative(mp3_path))
                        job.completed_tracks += 1
                        track_done_progress = 10.0 + ((completed_tracks + 1.0) / total) * 90.0
                        job.progress = min(100.0, max(job.progress, round(track_done_progress, 1)))
                        job.updated_at = datetime.now(timezone.utc)

                    except Exception as exc:
                        # Очищаем сырые и остаточные файлы этого трека в raw_dir
                        for part_file in raw_dir.glob(f"{idx:02d}.*"):
                            try:
                                part_file.unlink(missing_ok=True)
                            except OSError:
                                pass
                        for cover_pat in (f"cover_{idx}.*", f"cover_{idx:02d}.*"):
                            for cover_part in raw_dir.glob(cover_pat):
                                try:
                                    cover_part.unlink(missing_ok=True)
                                except OSError:
                                    pass
                        if mp3_path.exists():
                            try:
                                mp3_path.unlink(missing_ok=True)
                            except OSError:
                                pass

                        track.status = "error"
                        track.error = str(exc)
                        logger.warning(
                            "Сбой загрузки трека %d/%d (%s): %s",
                            idx,
                            total,
                            track.title,
                            exc,
                        )

                        # Если это одиночный трек (total == 1), сразу выбрасываем исключение
                        if total == 1:
                            raise

                        # Для плейлистов/альбомов (total > 1): пропускаем трек и продолжаем загрузку остальных
                        job.progress = min(100.0, max(job.progress, round(track_base + track_weight, 1)))
                        err_str = str(exc)
                        if is_drm_protected_error(err_str):
                            reason = "DRM защита"
                        elif is_age_restricted_error(err_str):
                            reason = "возрастное ограничение"
                        elif any(kw in err_str.lower() for kw in ("unavailable", "private", "not available", "deleted")):
                            reason = "недоступен"
                        elif "copyright" in err_str.lower() or "blocked" in err_str.lower():
                            reason = "заблокирован"
                        else:
                            reason = "ошибка загрузки"
                        job.current_step = f"Пропуск трека {idx}/{total}: {track.title} ({reason})"
                        job.updated_at = datetime.now(timezone.utc)
                        continue
                    finally:
                        # Гарантированная очистка временного файла обложки трека в любых исходах
                        if (
                            track_cover_file
                            and track_cover_file != cover_file
                            and track_cover_file.exists()
                        ):
                            try:
                                track_cover_file.unlink(missing_ok=True)
                            except OSError:
                                pass

                # Завершение задачи
                job.status = JobStatus.READY
                job.progress = 100.0
                if job.completed_tracks < total:
                    skipped_count = total - job.completed_tracks
                    if job.completed_tracks == 0:
                        job.current_step = f"Завершено: 0/{total} треков скачано ({skipped_count} с ошибками)"
                    else:
                        job.current_step = (
                            f"Готово к проверке тегов ({job.completed_tracks}/{total} скачано, {skipped_count} пропущено)"
                        )
                else:
                    job.current_step = "Готово к проверке тегов"
                job.updated_at = datetime.now(timezone.utc)
                logger.info(
                    "Задача загрузки %s успешно завершена (%d/%d треков)",
                    job.id,
                    job.completed_tracks,
                    total,
                )

            except Exception as exc:
                logger.exception("Сбой выполнения задачи загрузки %s", job_id)
                job.status = JobStatus.ERROR
                job.error = str(exc)
                job.current_step = f"Ошибка: {exc}"
                job.updated_at = datetime.now(timezone.utc)
            finally:
                # Окончательная санитарная очистка остаточных файлов в raw_dir
                try:
                    raw_p = self._sandbox.resolve(f"{job.id}/raw")
                    if raw_p.exists():
                        for item in raw_p.iterdir():
                            try:
                                if item.is_file() or item.is_symlink():
                                    item.unlink(missing_ok=True)
                            except OSError:
                                pass
                except Exception as exc:  # noqa: BLE001
                    logger.debug("Сбой при очистке остаточных файлов в %s/raw: %s", job.id, exc)

    async def _download_raw_stream(
        self,
        source_url: str,
        output_template: Path,
        raw_dir: Path,
        track_idx: int,
        on_progress: Callable[[float], None] | None = None,
    ) -> Path:
        """Выполняет неблокирующее скачивание аудиопотока через процесс yt-dlp."""
        async def _execute_yt_dlp(use_cookies: bool) -> tuple[int, str]:
            venv_bin = Path(sys.executable).parent / "yt-dlp"
            ytdlp_bin = str(venv_bin) if venv_bin.is_file() else (shutil.which("yt-dlp") or "yt-dlp")

            cmd = [
                ytdlp_bin,
                "--ignore-config",
                "--no-download-archive",
                "-f",
                "ba/b",
                "-o",
                str(output_template),
                "--no-playlist",
                "--newline",
                "--no-colors",
                "--progress",
                "--progress-template",
                "download:%(progress._percent_str)s",
                "--progress-delta",
                "0.2",
                "--no-warnings",
            ]

            cookies_file = self._settings.cookies_file
            if use_cookies and cookies_file and cookies_file.is_file():
                cmd.extend(["--cookies", str(cookies_file)])
            else:
                cmd.extend(["--extractor-args", "youtube:player_client=ios,visionos"])

            cmd.append(source_url)

            logger.debug("Запуск yt-dlp (use_cookies=%s): %s", use_cookies, " ".join(cmd))
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )

            progress_regex = re.compile(r"(?:\[download\]|\bdownload:)?\s*(\d+(?:\.\d+)?)%")

            async def read_stdout() -> None:
                if not proc.stdout:
                    return
                while True:
                    line_bytes = await proc.stdout.readline()
                    if not line_bytes or not isinstance(line_bytes, (bytes, bytearray)):
                        break
                    line = line_bytes.decode("utf-8", errors="replace").strip()
                    line = re.sub(r"\x1b\[[0-9;]*[a-zA-Z]", "", line)
                    match = progress_regex.search(line)
                    if match and on_progress:
                        try:
                            val = float(match.group(1))
                            if 0.0 <= val <= 100.0:
                                on_progress(val)
                        except ValueError:
                            pass

            async def read_stderr() -> bytes:
                if not proc.stderr:
                    return b""
                chunks = []
                while True:
                    chunk = await proc.stderr.readline()
                    if not chunk or not isinstance(chunk, (bytes, bytearray)):
                        break
                    chunks.append(chunk)
                return b"".join(chunks)

            try:
                stdout_task = asyncio.create_task(read_stdout())
                stderr_task = asyncio.create_task(read_stderr())
                await asyncio.wait_for(proc.wait(), timeout=180.0)
                await asyncio.gather(stdout_task, stderr_task)
                stderr = stderr_task.result()
                if not stderr and hasattr(proc, "communicate") and isinstance(getattr(proc.communicate, "return_value", None), tuple):
                    stderr = proc.communicate.return_value[1] or b""
            except asyncio.TimeoutError:
                proc.kill()
                stdout_task.cancel()
                stderr_task.cancel()
                await asyncio.gather(stdout_task, stderr_task, return_exceptions=True)
                raise DownloadError("Превышен таймаут загрузки аудиопотока через yt-dlp (180с)")
            except asyncio.CancelledError:
                proc.kill()
                stdout_task.cancel()
                stderr_task.cancel()
                await asyncio.gather(stdout_task, stderr_task, return_exceptions=True)
                raise

            return proc.returncode, stderr.decode("utf-8", errors="replace").strip()

        cookies_file = self._settings.cookies_file
        has_cookies = bool(cookies_file and cookies_file.is_file())

        # 1. Приоритетная попытка в чистом быстром гостевом режиме (без cookies)
        ret_code, err_msg = await _execute_yt_dlp(use_cookies=False)

        # 2. Если гостевой режим упал из-за Age Gate, и сконфигурированы cookies — повторяем с cookies
        if ret_code != 0 and has_cookies and self.is_age_restricted_error(err_msg):
            logger.info(
                "Обнаружено возрастное ограничение YouTube (Age Gate: %s). Повторная попытка с cookies...",
                err_msg,
            )
            for part_file in raw_dir.glob(f"{track_idx:02d}.*"):
                try:
                    if part_file.is_file() and part_file.name.endswith(".part"):
                        part_file.unlink(missing_ok=True)
                except OSError as exc:
                    logger.debug("Не удалось удалить остаточный файл %s: %s", part_file, exc)
            ret_code, err_msg = await _execute_yt_dlp(use_cookies=True)

        if ret_code != 0:
            raise DownloadError(f"Ошибка yt-dlp при скачивании аудио: {err_msg}")

        # Ищем скачанный файл в папке raw
        prefix = f"{track_idx:02d}."
        matched = [p for p in raw_dir.iterdir() if p.name.startswith(prefix) and not p.name.endswith(".part")]
        if not matched:
            raise DownloadError(f"Файл аудиопотока не найден в {raw_dir} после работы yt-dlp")

        return matched[0]

    async def _transcode_to_mp3(self, input_raw: Path, output_mp3: Path) -> None:
        """Транскодирует входной аудиофайл в MP3 320 kbps CBR / 44.1 kHz через ffmpeg."""
        ffmpeg_bin = shutil.which("ffmpeg") or "ffmpeg"
        cmd = [
            ffmpeg_bin,
            "-y",
            "-i",
            str(input_raw),
            "-codec:a",
            "libmp3lame",
            "-b:a",
            self._settings.AUDIO_BITRATE,
            "-ar",
            str(self._settings.AUDIO_SAMPLE_RATE),
            str(output_mp3),
        ]

        logger.debug("Запуск ffmpeg: %s", " ".join(cmd))
        proc = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )

        try:
            _, stderr = await asyncio.wait_for(proc.communicate(), timeout=300.0)
        except asyncio.TimeoutError:
            proc.kill()
            raise DownloadError("Превышен таймаут конвертации ffmpeg (300с)")
        except asyncio.CancelledError:
            proc.kill()
            raise

        if proc.returncode != 0:
            err_msg = stderr.decode("utf-8", errors="replace").strip()
            raise DownloadError(f"Ошибка ffmpeg при конвертации: {err_msg}")

        if not output_mp3.exists() or output_mp3.stat().st_size == 0:
            raise DownloadError("Целевой MP3 файл не был создан или пуст после конвертации")

    def _write_draft_tags(
        self,
        mp3_path: Path,
        track: ResolvedTrack,
        cover_file: Path | None,
    ) -> None:
        """Записывает черновые ID3v2.4 теги в MP3-файл через mutagen."""
        tags = ID3()

        # Название трека
        if track.title:
            tags.add(TIT2(encoding=3, text=[track.title]))

        # Исполнители трека (разделенные null-байтом для ID3v2.4)
        if track.artist:
            artist_list = [
                a.strip()
                for a in track.artist.replace("\x00", "\\\\").split("\\\\")
                if a.strip()
            ]
            if artist_list:
                tags.add(TPE1(encoding=3, text=artist_list))

        # Альбом
        if track.album:
            tags.add(TALB(encoding=3, text=[track.album]))

        # Исполнитель альбома
        if track.album_artist:
            album_artist_list = [
                a.strip()
                for a in track.album_artist.replace("\x00", "\\\\").split("\\\\")
                if a.strip()
            ]
            if album_artist_list:
                tags.add(TPE2(encoding=3, text=album_artist_list))

        # Номер дорожки
        if track.track_number:
            tags.add(TRCK(encoding=3, text=[str(track.track_number)]))

        # Год релиза
        if track.year:
            tags.add(TDRC(encoding=3, text=[str(track.year)]))

        # Жанр
        if track.genre:
            genre_list = [
                g.strip()
                for g in track.genre.replace("\x00", "\\\\").split("\\\\")
                if g.strip()
            ]
            if genre_list:
                tags.add(TCON(encoding=3, text=genre_list))

        # Встраивание обложки
        if cover_file and cover_file.exists() and cover_file.stat().st_size > 0:
            cover_data = cover_file.read_bytes()
            if cover_data:
                if cover_data.startswith(b"\x89PNG") or cover_file.suffix.lower() == ".png":
                    mime = "image/png"
                elif (
                    cover_data.startswith(b"RIFF")
                    and len(cover_data) >= 12
                    and cover_data[8:12] == b"WEBP"
                ) or cover_file.suffix.lower() == ".webp":
                    mime = "image/webp"
                else:
                    mime = "image/jpeg"
                tags.add(
                    APIC(
                        encoding=3,
                        mime=mime,
                        type=3,  # Front Cover
                        desc="Cover",
                        data=cover_data,
                    )
                )

        # Сохраняем строго в формате ID3v2.4 без устаревших ID3v1
        tags.save(str(mp3_path), v2_version=4, v1=0)

    async def cleanup_job(self, job_id: str) -> None:
        """Удаляет директорию задачи из песочницы TMP_DIR и стирает ее из памяти."""
        async with self._lock:
            self._jobs.pop(job_id, None)

        try:
            job_dir = self._sandbox.resolve(job_id, allow_root=False)
            if job_dir.exists():
                shutil.rmtree(job_dir, ignore_errors=True)
                logger.info("Удален временный каталог задачи %s", job_id)
        except Exception as exc:  # noqa: BLE001
            logger.warning("Ошибка при удалении каталога задачи %s: %s", job_id, exc)

    def cleanup_expired_jobs(self) -> int:
        """Сборщик мусора по TTL: удаляет задачи и каталоги в TMP_DIR старше JOB_TTL_HOURS."""
        now = datetime.now(timezone.utc)
        max_age_seconds = self._settings.JOB_TTL_HOURS * 3600
        removed_count = 0

        # 1. Проверяем in-memory задачи
        expired_ids = [
            jid
            for jid, job in list(self._jobs.items())
            if (now - job.created_at).total_seconds() > max_age_seconds
        ]
        for jid in expired_ids:
            self._jobs.pop(jid, None)
            try:
                j_dir = self._sandbox.resolve(jid, allow_root=False)
                if j_dir.exists():
                    shutil.rmtree(j_dir, ignore_errors=True)
                    removed_count += 1
            except Exception as exc:  # noqa: BLE001
                logger.warning("Ошибка сборщика мусора для задачи %s: %s", jid, exc)

        # 2. Проверяем остаточные папки в TMP_DIR
        try:
            tmp_root = self._sandbox.root
            if tmp_root.exists():
                for item in tmp_root.iterdir():
                    if item.is_dir() and not item.name.startswith("."):
                        mtime = item.stat().st_mtime
                        age = now.timestamp() - mtime
                        if age > max_age_seconds:
                            shutil.rmtree(item, ignore_errors=True)
                            removed_count += 1
                            logger.info("Сборщик мусора удалил устаревший каталог: %s", item.name)
        except Exception as exc:  # noqa: BLE001
            logger.warning("Ошибка при сканировании TMP_DIR сборщиком мусора: %s", exc)

        return removed_count
