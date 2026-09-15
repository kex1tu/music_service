"""Сервис интеграции нативного Go CLI-сканера медиатеки (Native Scanner Service).

Обеспечивает вызов скомпилированного бинарника music-scanner для параллельной,
сверхбыстрой потоковой индексации MP3-файлов через IPC (NDJSON).
При отсутствии бинарника или ошибках выполнения обеспечивает прозрачный откат
на стандартный Python-конвейер os.walk + mutagen.
"""

import asyncio
import logging
import os
from collections.abc import AsyncGenerator
from pathlib import Path
from typing import Any

try:
    import orjson

    def loads_json(data: bytes | str) -> dict[str, Any]:
        return orjson.loads(data)
except ImportError:
    import json

    def loads_json(data: bytes | str) -> dict[str, Any]:
        return json.loads(data)

from app.core.sandbox import PathSandbox

logger = logging.getLogger("music_manager.services.native_scanner")

PROJECT_ROOT = Path(__file__).resolve().parent.parent.parent


class NativeScannerService:
    """Адаптер для запуска внешнего Go-бинарника music-scanner и чтения потока NDJSON."""

    def __init__(self, music_sandbox: PathSandbox, bin_path: Path | None = None) -> None:
        self._sandbox = music_sandbox
        if bin_path:
            self._bin_path = bin_path.expanduser().resolve()
        else:
            self._bin_path = (PROJECT_ROOT / "bin" / "music-scanner").resolve()

    @property
    def bin_path(self) -> Path:
        """Возвращает путь к бинарнику сканера."""
        return self._bin_path

    @property
    def is_available(self) -> bool:
        """Проверяет наличие и права на исполнение Go-бинарника."""
        try:
            return self._bin_path.exists() and self._bin_path.is_file() and os.access(self._bin_path, os.X_OK)
        except OSError:
            return False

    async def scan_stream(self, db_path: Path | None = None) -> AsyncGenerator[dict[str, Any], None]:
        """Асинхронно запускает процесс music-scanner и стримит распарсенные метаданные треков."""
        if not self.is_available:
            logger.debug("Нативный Go-сканер недоступен по пути %s", self._bin_path)
            return

        safe_root = self._sandbox.root
        if not safe_root.exists() or not safe_root.is_dir():
            logger.debug("Корневая директория медиатеки не существует: %s", safe_root)
            return

        cmd = [str(self._bin_path), "--root", str(safe_root)]
        if db_path:
            cmd.extend(["--db", str(db_path)])

        logger.info("Запуск нативного Go-сканера: %s", " ".join(cmd))

        try:
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
        except Exception as exc:  # noqa: BLE001
            logger.warning("Не удалось запустить процесс Go-сканера: %s", exc)
            return

        assert proc.stdout is not None
        assert proc.stderr is not None

        stderr_task = asyncio.create_task(proc.stderr.read())

        try:
            while True:
                line = await asyncio.wait_for(proc.stdout.readline(), timeout=30.0)
                if not line:
                    break

                line_clean = line.strip()
                if not line_clean:
                    continue

                try:
                    data = loads_json(line_clean)
                    yield data
                except Exception as exc:  # noqa: BLE001
                    logger.debug("Ошибка десериализации строки NDJSON: %s (%s)", line_clean[:100], exc)
        except asyncio.TimeoutError:
            logger.error("Таймаут чтения из Go-сканера (30с), завершение процесса")
            try:
                proc.kill()
            except ProcessLookupError:
                pass
            raise
        finally:
            if proc.returncode is None:
                try:
                    proc.kill()
                except ProcessLookupError:
                    pass
            stderr_rem = await stderr_task
            await proc.wait()
            if proc.returncode != 0 and proc.returncode != -9:
                err_msg = stderr_rem.decode("utf-8", errors="replace").strip() if stderr_rem else ""
                logger.error("Go-сканер завершился с кодом %d: %s", proc.returncode, err_msg)
            elif proc.returncode == 0:
                logger.info("Нативный Go-сканер успешно завершил индексацию медиатеки")
