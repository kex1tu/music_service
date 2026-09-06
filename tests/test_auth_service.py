"""Тесты для сервиса аутентификации app/services/auth_service.py."""

import pytest

from app.config import Settings
from app.core.exceptions import AuthenticationError
from app.core.security import create_session_token
from app.services.auth_service import AuthService
from tests.conftest import TEST_ADMIN_PASSWORD


def test_verify_credentials_success(test_settings: Settings):
    """Проверка успешной валидации учетных данных администратора."""
    service = AuthService(test_settings)
    assert service.verify_credentials("admin", TEST_ADMIN_PASSWORD) is True


def test_verify_credentials_wrong_password(test_settings: Settings):
    """Проверка отказа при неверном пароле."""
    service = AuthService(test_settings)
    assert service.verify_credentials("admin", "wrong_password_123") is False


def test_verify_credentials_wrong_login(test_settings: Settings):
    """Проверка отказа при неверном логине (защита Timing Attack)."""
    service = AuthService(test_settings)
    assert service.verify_credentials("unknown_user", TEST_ADMIN_PASSWORD) is False


def test_verify_credentials_both_wrong(test_settings: Settings):
    """Проверка отказа при неверных логине и пароле."""
    service = AuthService(test_settings)
    assert service.verify_credentials("bad_login", "bad_password") is False


def test_create_and_verify_session_token(test_settings: Settings):
    """Проверка генерации и верификации токена сессии."""
    service = AuthService(test_settings)
    token = service.create_session_token("admin")

    assert isinstance(token, str)
    username = service.verify_session_token(token)
    assert username == "admin"


def test_verify_session_token_expired(test_settings: Settings):
    """Проверка выброса AuthenticationError при истечении срока жизни токена."""
    service = AuthService(test_settings)
    token = service.create_session_token("admin")

    # Имитируем просроченный токен через нулевой/отрицательный SESSION_TTL
    expired_settings = test_settings.model_copy(update={"SESSION_TTL": -1})
    expired_service = AuthService(expired_settings)

    with pytest.raises(AuthenticationError) as exc_info:
        expired_service.verify_session_token(token)

    assert exc_info.value.status_code == 401
    assert "истек" in exc_info.value.message


def test_verify_session_token_invalid_signature(test_settings: Settings):
    """Проверка выброса AuthenticationError при поврежденной подписи токена."""
    service = AuthService(test_settings)
    token = service.create_session_token("admin")

    with pytest.raises(AuthenticationError) as exc_info:
        service.verify_session_token(token + "_tampered")

    assert exc_info.value.status_code == 401
    assert "подпись" in exc_info.value.message


def test_verify_session_token_missing_sub(test_settings: Settings):
    """Проверка выброса AuthenticationError при отсутствии 'sub' в токене."""
    service = AuthService(test_settings)
    # Создаем токен без поля 'sub'
    token = create_session_token({"other_key": "val"}, test_settings.SECRET_KEY)

    with pytest.raises(AuthenticationError) as exc_info:
        service.verify_session_token(token)

    assert exc_info.value.status_code == 401
    assert "идентификатора" in exc_info.value.message
