"""Внедрение зависимостей (Dependency Injection) для FastAPI.

Предоставляет фабрики и провайдеры зависимостей для эндпоинтов и сервисов,
включая валидацию сессий и извлечение текущего пользователя.
"""

from functools import lru_cache
from typing import Annotated

from fastapi import Depends, Request

from app.config import Settings
from app.core.exceptions import AuthenticationError
from app.core.sandbox import PathSandbox
from app.services.auth_service import AuthService


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    """Возвращает кэшированный экземпляр конфигурации приложения."""
    return Settings()


# Типизированные аннотации зависимостей (FastAPI idiomatic)
SettingsDep = Annotated[Settings, Depends(get_settings)]


def get_auth_service(settings: SettingsDep) -> AuthService:
    """Провайдер экземпляра сервиса аутентификации AuthService."""
    return AuthService(settings)


AuthServiceDep = Annotated[AuthService, Depends(get_auth_service)]


def extract_token_from_request(request: Request, settings: Settings) -> str | None:
    """Извлекает сессионный токен из cookie или заголовка Authorization.

    Поддерживает:
    1. Сессионный cookie (settings.SESSION_COOKIE_NAME) для браузера.
    2. Заголовок 'Authorization: Bearer <token>' для API-клиентов и тестов.
    """
    # 1. Проверяем наличие сессионного cookie
    token = request.cookies.get(settings.SESSION_COOKIE_NAME)
    if token:
        return token

    # 2. Проверяем заголовок Authorization: Bearer <token>
    auth_header = request.headers.get("Authorization")
    if auth_header and auth_header.startswith("Bearer "):
        bearer_token = auth_header[7:].strip()
        if bearer_token:
            return bearer_token

    return None


def get_current_user(
    request: Request,
    auth_service: AuthServiceDep,
    settings: SettingsDep,
) -> str:
    """Зависимость FastAPI для проверки аутентификации пользователя.

    Извлекает токен сессии, проверяет его подпись и срок жизни через AuthService.
    Возвращает логин пользователя или выбрасывает AuthenticationError (HTTP 401).
    """
    token = extract_token_from_request(request, settings)
    if not token:
        raise AuthenticationError("Требуется аутентификация")

    return auth_service.verify_session_token(token)


CurrentUserDep = Annotated[str, Depends(get_current_user)]


def get_music_sandbox(settings: SettingsDep) -> PathSandbox:
    """Провайдер экземпляра PathSandbox для постоянной медиатеки MUSIC_DIR."""
    return PathSandbox(settings.MUSIC_DIR)


def get_tmp_sandbox(settings: SettingsDep) -> PathSandbox:
    """Провайдер экземпляра PathSandbox для временного буфера загрузок TMP_DIR."""
    return PathSandbox(settings.TMP_DIR)


MusicSandboxDep = Annotated[PathSandbox, Depends(get_music_sandbox)]
TmpSandboxDep = Annotated[PathSandbox, Depends(get_tmp_sandbox)]


