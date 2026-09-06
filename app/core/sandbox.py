"""Компонент изоляции файловой системы (Path Sandbox Guard).

Обеспечивает валидацию, канонизацию и безопасное разрешение относительных путей
внутри заданного корневого каталога. Предотвращает уязвимости Path Traversal,
включая обход через относительные переходы (../), URL-кодирование (%2e%2e),
инъекции нулевого байта (\\x00) и символические ссылки, ведущие во внешнюю файловую систему.

В соответствии с AGENTS.md, это строгий cross-cutting инвариант: любая файловая операция
в сервисах обязана оперировать путями, разрешенными исключительно через PathSandbox.resolve().
"""

import re
import urllib.parse
from pathlib import Path

from app.core.exceptions import PathTraversalError, StorageConfigurationError


class PathSandbox:
    """Охранный страж путей файловой системы для безопасной работы с каталогами."""

    # Запрещенные символы для одиночных имен файлов (санитизация в файловых операциях)
    DANGEROUS_FILENAME_CHARS = re.compile(r'[\x00/\\:*?"<>|]')

    def __init__(self, root: Path | str) -> None:
        """Инициализирует песочницу с проверкой валидности корневого пути.

        Args:
            root: Корневой путь песочницы (будет приведен к абсолютному каноническому виду).

        Raises:
            StorageConfigurationError: Если корневой путь пуст или указывает на некорректный объект.
        """
        if not root:
            raise StorageConfigurationError("Корневой каталог песочницы не может быть пустым")

        resolved_root = Path(root).expanduser().resolve()
        if resolved_root.exists() and not resolved_root.is_dir():
            raise StorageConfigurationError(
                f"Корневой путь песочницы '{resolved_root}' существует, но не является директорией"
            )

        self._root = resolved_root

    @property
    def root(self) -> Path:
        """Возвращает абсолютный канонический путь корня песочницы."""
        return self._root

    def resolve(self, relative_path: str | Path, allow_root: bool = True) -> Path:
        """Безопасно разрешает переданный относительный путь внутри корня песочницы.

        Алгоритм:
        1. Проверка типов и детектирование нулевых байтов (\\x00).
        2. Итеративное URL-декодирование (защита от двойного кодирования %252e%252e).
        3. Замена обратных слэшей '\\\\' на '/' и отсечение ведущих слэшей.
        4. Разрешение пути через resolve(strict=False) с раскрытием symlinks.
        5. Строгая проверка containment через is_relative_to(self.root).
        6. Контроль доступа к корню при allow_root=False.

        Args:
            relative_path: Относительный путь от корня.
            allow_root: Разрешено ли возвращать сам корень песочницы.

        Returns:
            Абсолютный валидированный Path внутри корня песочницы.

        Raises:
            PathTraversalError: При попытке выхода за пределы корня, некорректных символах
                                или попытке доступа к корню при allow_root=False.
        """
        if relative_path is None:
            raise PathTraversalError("Путь не может быть пустым (None)")

        raw = str(relative_path)
        if "\0" in raw:
            raise PathTraversalError("Обнаружен нулевой байт (Null-byte injection)")

        # Итеративное URL-декодирование (до стабилизации, макс. 3 прохода)
        decoded = raw
        for _ in range(3):
            next_decoded = urllib.parse.unquote(decoded)
            if next_decoded == decoded:
                break
            decoded = next_decoded

        if "\0" in decoded:
            raise PathTraversalError("Обнаружен нулевой байт после URL-декодирования")

        # Нормализация слэшей и удаление ведущих разделителей (защита от Python Path trap)
        clean = decoded.replace("\\", "/").lstrip("/")

        # Обработка корня
        if clean == "" or clean == ".":
            if not allow_root:
                raise PathTraversalError("Доступ к корню песочницы запрещен для данной операции")
            return self._root

        candidate = self._root.joinpath(clean)
        try:
            resolved = candidate.resolve(strict=False)
        except Exception as exc:
            raise PathTraversalError(f"Ошибка канонизации пути '{relative_path}': {exc}") from exc

        # Проверка изоляции
        if not resolved.is_relative_to(self._root):
            raise PathTraversalError(
                f"Попытка выхода за пределы песочницы: '{relative_path}' разрешен в '{resolved}'"
            )

        if not allow_root and resolved == self._root:
            raise PathTraversalError("Доступ к корню песочницы запрещен для данной операции")

        return resolved

    def resolve_child(self, relative_path: str | Path) -> Path:
        """Разрешает путь строго к дочернему объекту (доступ к самому корню запрещен)."""
        return self.resolve(relative_path, allow_root=False)

    def to_relative(self, absolute_path: Path | str) -> Path:
        """Преобразует абсолютный путь файловой системы в безопасный относительный путь от корня.

        Используется для сериализации данных клиенту без раскрытия серверных путей.

        Raises:
            PathTraversalError: Если переданный путь лежит вне корня песочницы.
        """
        resolved = Path(absolute_path).expanduser().resolve(strict=False)
        if not resolved.is_relative_to(self._root):
            raise PathTraversalError(
                f"Путь '{resolved}' не принадлежит корню песочницы '{self._root}'"
            )
        return resolved.relative_to(self._root)

    def is_safe(self, relative_path: str | Path, allow_root: bool = True) -> bool:
        """Проверяет безопасность пути без возбуждения исключений (True/False)."""
        try:
            self.resolve(relative_path, allow_root=allow_root)
            return True
        except PathTraversalError:
            return False

    @classmethod
    def validate_filename(cls, filename: str) -> str:
        """Проверяет корректность одиночного имени файла (без компонентов пути).

        Запрещает слэши, двоеточия, спецсимволы и пустые имена.
        """
        if not filename or filename in (".", ".."):
            raise PathTraversalError(f"Недопустимое имя файла: '{filename}'")
        if cls.DANGEROUS_FILENAME_CHARS.search(filename):
            raise PathTraversalError(
                f"Имя файла '{filename}' содержит недопустимые символы (/ \\ : * ? \" < > | или null)"
            )
        return filename
