"""Модуль криптографических примитивов безопасности.

Обеспечивает хеширование и верификацию паролей через bcrypt,
а также генерацию и декодирование криптографически подписанных
сессионных токенов через itsdangerous.
"""

import logging
from typing import Any

import bcrypt
from itsdangerous import URLSafeTimedSerializer

logger = logging.getLogger("music_manager.security")

# Заранее вычисленный валидный bcrypt-хэш для предотвращения атак по времени (Timing Attack Defense)
DUMMY_BCRYPT_HASH = "$2b$12$DckMFRHl/6N/4AkviUVPoOI7Yo1besbb6Vjhn4kxF7I3fb/EeFiFO"
SESSION_SALT = "music-manager-session-salt"


def verify_password(plain_password: str, hashed_password: str) -> bool:
    """Сверяет открытый пароль с bcrypt-хэшем.

    Безопасно перехватывает ValueError/TypeError при поврежденном или
    некорректном формате хэша, возвращая False.
    """
    try:
        return bcrypt.checkpw(
            plain_password.encode("utf-8"),
            hashed_password.encode("utf-8"),
        )
    except (ValueError, TypeError) as exc:
        logger.warning("Некорректный формат хэша пароля: %s", exc)
        return False


def get_password_hash(password: str) -> str:
    """Генерирует bcrypt-хэш пароля с автоматической генерацией соли."""
    salt = bcrypt.gensalt(rounds=12)
    return bcrypt.hashpw(password.encode("utf-8"), salt).decode("utf-8")


def create_session_token(payload: dict[str, Any], secret_key: str) -> str:
    """Создает криптографически подписанный сессионный токен с временной меткой."""
    serializer = URLSafeTimedSerializer(secret_key, salt=SESSION_SALT)
    return serializer.dumps(payload)


def decode_session_token(token: str, secret_key: str, max_age: int) -> dict[str, Any]:
    """Проверяет подпись и срок жизни сессионного токена.

    При нарушении целостности подписи выбрасывает BadSignature.
    При истечении срока жизни токена выбрасывает SignatureExpired.
    """
    serializer = URLSafeTimedSerializer(secret_key, salt=SESSION_SALT)
    return serializer.loads(token, max_age=max_age)
