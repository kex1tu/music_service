"""Тесты для модуля криптографических примитивов app/core/security.py."""

import time

import pytest
from itsdangerous import BadSignature, SignatureExpired

from app.core.security import (
    create_session_token,
    decode_session_token,
    get_password_hash,
    verify_password,
)


def test_password_hashing_and_verification():
    """Проверка генерации хэша и успешной верификации пароля."""
    password = "SuperSecretPassword123!"
    hashed = get_password_hash(password)

    assert isinstance(hashed, str)
    assert hashed.startswith("$2b$")
    assert verify_password(password, hashed) is True
    assert verify_password("WrongPassword", hashed) is False
    assert verify_password("", hashed) is False


def test_verify_password_invalid_hash():
    """Проверка устойчивости verify_password к поврежденным и некорректным хэшам."""
    assert verify_password("secret", "invalid_hash_string") is False
    assert verify_password("secret", "") is False
    assert verify_password("secret", "$2b$12$short") is False


def test_session_token_creation_and_decoding():
    """Проверка создания и успешной расшифровки валидного сессионного токена."""
    secret_key = "test_secret_key_1234567890123456"
    payload = {"sub": "admin", "role": "superuser"}

    token = create_session_token(payload, secret_key)
    assert isinstance(token, str)

    decoded = decode_session_token(token, secret_key, max_age=3600)
    assert decoded["sub"] == "admin"
    assert decoded["role"] == "superuser"


def test_session_token_expired():
    """Проверка генерации SignatureExpired при истечении срока действия токена."""
    secret_key = "test_secret_key_1234567890123456"
    payload = {"sub": "admin"}

    token = create_session_token(payload, secret_key)
    time.sleep(0.05)

    with pytest.raises(SignatureExpired):
        decode_session_token(token, secret_key, max_age=-1)


def test_session_token_bad_signature():
    """Проверка генерации BadSignature при нарушении целостности токена или неверном ключе."""
    secret_key = "test_secret_key_1234567890123456"
    other_key = "another_different_secret_key_1234"
    payload = {"sub": "admin"}

    token = create_session_token(payload, secret_key)

    # Попытка проверки с другим ключом
    with pytest.raises(BadSignature):
        decode_session_token(token, other_key, max_age=3600)

    # Попытка проверки поврежденного токена
    with pytest.raises(BadSignature):
        decode_session_token(token + "corrupted", secret_key, max_age=3600)
