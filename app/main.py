"""Точка входа ASGI и сборка приложения Music Downloader & Tag Manager.

Инициализирует FastAPI приложение, регистрирует middleware,
глобальные обработчики ошибок, статические файлы и lifespan-проверки.
В соответствии с инвариантами AGENTS.md, файл содержит только сборку
компонентов (DI) и не содержит доменной бизнес-логики.
"""

import logging
import uuid
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import HTMLResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates
from starlette.middleware.sessions import SessionMiddleware

from app.config import Settings
from app.core.exceptions import MusicManagerError, StorageConfigurationError
from app.dependencies import get_settings
from app.routers.auth import router as auth_router

logger = logging.getLogger("music_manager")


def verify_storage_directory(directory: Path, name: str) -> None:
    """Проверяет доступность каталога для чтения и записи с помощью контрольной пробы.

    Создает каталог при его отсутствии и выполняет атомарную запись/удаление
    пробного файла. При сбое прав доступа или ошибке ввода-вывода выбрасывает
    StorageConfigurationError (Fail-Fast).
    """
    try:
        directory.mkdir(parents=True, exist_ok=True)
        probe_file = directory / f".probe_{uuid.uuid4().hex}.tmp"
        probe_file.write_text("probe_rw_test", encoding="utf-8")
        probe_file.unlink()
    except Exception as exc:
        raise StorageConfigurationError(
            f"Каталог {name} ({directory}) недоступен для записи: {exc}"
        ) from exc


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Управление жизненным циклом приложения FastAPI."""
    settings: Settings = getattr(app.state, "settings", None) or get_settings()

    # Fail-fast проверка хранилищ при старте
    verify_storage_directory(settings.MUSIC_DIR, "MUSIC_DIR")
    verify_storage_directory(settings.TMP_DIR, "TMP_DIR")
    logger.info(
        "Хранилища инициализированы: MUSIC_DIR=%s, TMP_DIR=%s",
        settings.MUSIC_DIR,
        settings.TMP_DIR,
    )

    yield

    logger.info("Остановка приложения Music Downloader & Tag Manager.")


def create_app(settings: Settings | None = None) -> FastAPI:
    """Фабрика создания экземпляра FastAPI приложения.

    Позволяет изолированно передавать тестовые настройки в тестах.
    """
    app_settings = settings or get_settings()

    application = FastAPI(
        title="Music Downloader & Tag Manager",
        version="1.0.0",
        lifespan=lifespan,
    )
    application.state.settings = app_settings
    application.dependency_overrides[get_settings] = lambda: app_settings

    # Подключение SessionMiddleware с защищенными параметрами
    application.add_middleware(
        SessionMiddleware,
        secret_key=app_settings.SECRET_KEY,
        session_cookie=app_settings.SESSION_COOKIE_NAME,
        max_age=app_settings.SESSION_TTL,
        same_site=app_settings.SESSION_COOKIE_SAMESITE,
        https_only=app_settings.SESSION_COOKIE_SECURE,
    )

    # Регистрация глобальных обработчиков исключений
    @application.exception_handler(MusicManagerError)
    async def handle_music_manager_error(
        request: Request, exc: MusicManagerError
    ) -> JSONResponse:
        logger.warning(
            "Доменная ошибка [%s] на %s: %s",
            exc.error_code,
            request.url.path,
            exc.message,
        )
        return JSONResponse(
            status_code=exc.status_code,
            content=exc.to_dict(),
        )

    @application.exception_handler(RequestValidationError)
    async def handle_validation_error(
        request: Request, exc: RequestValidationError
    ) -> JSONResponse:
        logger.warning("Ошибка валидации запроса на %s: %s", request.url.path, exc)
        return JSONResponse(
            status_code=422,
            content={
                "error": "VALIDATION_ERROR",
                "message": "Ошибка валидации параметров запроса",
                "details": exc.errors(),
            },
        )

    @application.exception_handler(Exception)
    async def handle_unhandled_exception(
        request: Request, exc: Exception
    ) -> JSONResponse:
        # Логируем полный трейс на сервере, но скрываем детали от клиента (инвариант AGENTS.md)
        logger.exception(
            "Необработанное исключение при обработке запроса %s: %s",
            request.url.path,
            exc,
        )
        return JSONResponse(
            status_code=500,
            content={
                "error": "INTERNAL_SERVER_ERROR",
                "message": "Внутренняя ошибка сервера",
            },
        )

    # Монтирование статических файлов
    static_dir = Path(__file__).resolve().parent / "static"
    static_dir.mkdir(parents=True, exist_ok=True)
    application.mount("/static", StaticFiles(directory=str(static_dir)), name="static")

    # Шаблоны Jinja2
    templates_dir = Path(__file__).resolve().parent / "templates"
    templates_dir.mkdir(parents=True, exist_ok=True)
    templates = Jinja2Templates(directory=str(templates_dir))

    # Базовые маршруты
    @application.get("/api/health")
    async def health_check():
        """Проверка жизнеспособности сервиса."""
        return {
            "status": "ok",
            "app": "music-manager",
            "version": "1.0.0",
        }

    @application.get("/", response_class=HTMLResponse)
    async def index(request: Request):
        """Рендеринг главной страницы."""
        return templates.TemplateResponse(request=request, name="index.html")

    # Регистрация роутеров
    application.include_router(auth_router)

    return application


# Канонический экземпляр приложения для ASGI сервера (uvicorn app.main:app)
app = create_app()
