"""Точка входа ASGI и сборка приложения Music Downloader & Tag Manager.

Инициализирует FastAPI приложение, регистрирует middleware,
глобальные обработчики ошибок, статические файлы и lifespan-проверки.
В соответствии с инвариантами AGENTS.md, файл содержит только сборку
компонентов (DI) и не содержит доменной бизнес-логики.
"""

import asyncio
import logging
import uuid
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI, Request
from fastapi.encoders import jsonable_encoder
from fastapi.exceptions import RequestValidationError
from fastapi.responses import HTMLResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates
from starlette.middleware.sessions import SessionMiddleware

from app.config import Settings
from app.core.exceptions import MusicManagerError, StorageConfigurationError
from app.dependencies import get_settings
from app.routers.auth import router as auth_router
from app.routers.downloads import router as downloads_router
from app.routers.files import router as files_router
from app.routers.lyrics import router as lyrics_router
from app.routers.tags import router as tags_router

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

    # Фоновая задача периодической очистки устаревших загрузок по TTL (раз в час)
    async def _periodic_cleanup():
        while True:
            try:
                await asyncio.sleep(3600)
                from app.dependencies import (
                    get_deezer_client,
                    get_download_service,
                    get_source_resolver,
                    get_tmp_sandbox,
                )

                dc = get_deezer_client()
                sr = get_source_resolver(dc, settings)
                sb = get_tmp_sandbox(settings)
                svc = get_download_service(settings, sb, sr, dc)
                cleaned = svc.cleanup_expired_jobs()
                if cleaned > 0:
                    logger.info("Сборщик мусора удалил %d устаревших задач/каталогов", cleaned)
            except asyncio.CancelledError:
                break
            except Exception as exc:  # noqa: BLE001
                logger.warning("Сбой при фоновой очистке TMP_DIR: %s", exc)

    cleanup_task = asyncio.create_task(_periodic_cleanup())

    try:
        yield
    finally:
        cleanup_task.cancel()
        try:
            await cleanup_task
        except asyncio.CancelledError:
            pass

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

    # Middleware для отключения кэширования динамических API-ответов в браузерах
    @application.middleware("http")
    async def add_no_cache_headers(request: Request, call_next):
        response = await call_next(request)
        if not request.url.path.startswith("/static/"):
            response.headers["Cache-Control"] = "no-store, no-cache, must-revalidate, max-age=0"
            response.headers["Pragma"] = "no-cache"
            response.headers["Expires"] = "0"
        return response

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
                "details": jsonable_encoder(exc.errors()),
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
    application.include_router(downloads_router)
    application.include_router(tags_router)
    application.include_router(lyrics_router)
    application.include_router(files_router)

    return application


# Канонический экземпляр приложения для ASGI сервера (uvicorn app.main:app)
app = create_app()
