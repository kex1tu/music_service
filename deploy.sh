#!/usr/bin/env bash
# ==============================================================================
# Production Deployment Script: Music Manager & Navidrome + Nginx & SSL
# Supported OS: Ubuntu 22.04+ / Debian 12+
# ==============================================================================

set -euo pipefail

# ------------------------------------------------------------------------------
# 0. Конфигурационные переменные (Настройте под свои нужды)
# ------------------------------------------------------------------------------
APP_DIR="/opt/music-manager"
MUSIC_DIR="/var/lib/music"
NAVIDROME_DIR="/opt/navidrome"
NAVIDROME_DATA_DIR="/var/lib/navidrome"

# Домены для Nginx (оставьте пустыми, если SSL/Nginx настраивается позже вручную)
ENABLE_NGINX="${ENABLE_NGINX:-false}"
DOMAIN_MM="${DOMAIN_MM:-}"         # например: manager.example.com
DOMAIN_ND="${DOMAIN_ND:-}"         # например: music.example.com
CERTBOT_EMAIL="${CERTBOT_EMAIL:-}" # например: admin@example.com

# Если включен Nginx, сервисы привязываются строго к localhost (127.0.0.1)
if [[ "${ENABLE_NGINX}" == "true" && -n "${DOMAIN_MM}" && -n "${DOMAIN_ND}" ]]; then
    MM_HOST="127.0.0.1"
    ND_HOST="127.0.0.1"
    COOKIE_SECURE="true"
else
    MM_HOST="0.0.0.0"
    ND_HOST="0.0.0.0"
    COOKIE_SECURE="false"
fi

MM_PORT="8010"
ND_PORT="4533"

# Системные пользователи и группа доступа к медиатеке
MEDIA_GROUP="media"
MM_USER="music"
ND_USER="navidrome"

# Версия Navidrome (официальный бинарный релиз)
NAVIDROME_VERSION="0.54.5"

# Учетные данные администратора Music Manager
ADMIN_LOGIN="admin"
# Если пароль не указан в окружении, сгенерируем безопасный случайный пароль
ADMIN_PASSWORD="${ADMIN_PASSWORD:-$(head -c 16 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 16)}"

# Цвета для вывода в терминал
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

# ------------------------------------------------------------------------------
# 1. Проверка прав суперпользователя (Root check)
# ------------------------------------------------------------------------------
if [[ "${EUID}" -ne 0 ]]; then
    echo -e "${RED}[ERROR] Скрипт должен быть запущен с правами root (sudo ./deploy.sh)${NC}" >&2
    exit 1
fi

echo -e "${BLUE}=== Запуск установки Music Manager & Navidrome ===${NC}"

# ------------------------------------------------------------------------------
# 2. Установка системных зависимостей
# ------------------------------------------------------------------------------
echo -e "\n${YELLOW}[1/8] Установка системных пакетов...${NC}"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
PACKAGES=(
    curl
    tar
    git
    ffmpeg
    python3
    python3-venv
    python3-pip
    python3-dev
    build-essential
    acl
)

if [[ "${ENABLE_NGINX}" == "true" ]]; then
    PACKAGES+=(nginx certbot python3-certbot-nginx)
fi

apt-get install -y -qq "${PACKAGES[@]}"

# ------------------------------------------------------------------------------
# 3. Создание пользователей и единой группы доступа media
# ------------------------------------------------------------------------------
echo -e "\n${YELLOW}[2/8] Настройка пользователей и групп...${NC}"

# Создаем группу media, если ее нет
if ! getent group "${MEDIA_GROUP}" >/dev/null 2>&1; then
    groupadd -r "${MEDIA_GROUP}"
    echo "Группа '${MEDIA_GROUP}' создана."
fi

# Пользователь для Music Manager
if ! id -u "${MM_USER}" >/dev/null 2>&1; then
    useradd -r -s /usr/sbin/nologin -g "${MEDIA_GROUP}" -d "${APP_DIR}" "${MM_USER}"
    echo "Пользователь '${MM_USER}' создан."
else
    usermod -aG "${MEDIA_GROUP}" "${MM_USER}"
fi

# Пользователь для Navidrome
if ! id -u "${ND_USER}" >/dev/null 2>&1; then
    useradd -r -s /usr/sbin/nologin -g "${MEDIA_GROUP}" -d "${NAVIDROME_DATA_DIR}" "${ND_USER}"
    echo "Пользователь '${ND_USER}' создан."
else
    usermod -aG "${MEDIA_GROUP}" "${ND_USER}"
fi

# ------------------------------------------------------------------------------
# 4. Настройка общей директории медиатеки (MUSIC_DIR) с SGID
# ------------------------------------------------------------------------------
echo -e "\n${YELLOW}[3/8] Инициализация общей директории музыки: ${MUSIC_DIR}...${NC}"
mkdir -p "${MUSIC_DIR}"
chown -R "${MM_USER}:${MEDIA_GROUP}" "${MUSIC_DIR}"
# 2775 (setgid): новые папки автоматически наследуют группу media
chmod 2775 "${MUSIC_DIR}"

# ------------------------------------------------------------------------------
# 5. Развертывание Music Manager (Python venv + dependencies)
# ------------------------------------------------------------------------------
echo -e "\n${YELLOW}[4/8] Развертывание Music Manager в ${APP_DIR}...${NC}"
mkdir -p "${APP_DIR}" "${APP_DIR}/tmp_downloads"

CURRENT_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Если скрипт запущен внутри репозитория проекта, синхронизируем файлы
if [[ -f "${CURRENT_SCRIPT_DIR}/app/main.py" && "${CURRENT_SCRIPT_DIR}" != "${APP_DIR}" ]]; then
    echo "Копирование исходных файлов из ${CURRENT_SCRIPT_DIR} в ${APP_DIR}..."
    cp -r "${CURRENT_SCRIPT_DIR}/app" "${APP_DIR}/"
    cp -r "${CURRENT_SCRIPT_DIR}/requirements.txt" "${APP_DIR}/"
    [[ -f "${CURRENT_SCRIPT_DIR}/music-manager.service" ]] && cp "${CURRENT_SCRIPT_DIR}/music-manager.service" "${APP_DIR}/"
fi

# Инициализация venv
if [[ ! -d "${APP_DIR}/venv" ]]; then
    echo "Создание виртуального окружения Python..."
    python3 -m venv "${APP_DIR}/venv"
fi

echo "Установка Python-библиотек из requirements.txt..."
"${APP_DIR}/venv/bin/pip" install --upgrade pip setuptools wheel -q
"${APP_DIR}/venv/bin/pip" install -r "${APP_DIR}/requirements.txt" -q

# Генерация .env конфигурации
if [[ ! -f "${APP_DIR}/.env" ]]; then
    echo "Генерация секретов и файла .env для Music Manager..."
    
    SECRET_KEY="$("${APP_DIR}/venv/bin/python" -c "import secrets; print(secrets.token_hex(32))")"
    ADMIN_HASH="$("${APP_DIR}/venv/bin/python" -c "import bcrypt; print(bcrypt.hashpw(b'''${ADMIN_PASSWORD}''', bcrypt.gensalt()).decode())")"

    cat > "${APP_DIR}/.env" <<EOF
# ==============================================================================
# Music Downloader & Tag Manager — Production Environment
# ==============================================================================
MUSIC_DIR=${MUSIC_DIR}
TMP_DIR=${APP_DIR}/tmp_downloads

SECRET_KEY=${SECRET_KEY}
ADMIN_LOGIN=${ADMIN_LOGIN}
ADMIN_PASSWORD_HASH=${ADMIN_HASH}

SESSION_TTL=604800
SESSION_COOKIE_NAME=music_manager_session
SESSION_COOKIE_SECURE=${COOKIE_SECURE}
SESSION_COOKIE_SAMESITE=strict

AUDIO_BITRATE=320k
AUDIO_SAMPLE_RATE=44100
MAX_CONCURRENT_DOWNLOADS=2
JOB_TTL_HOURS=24

HOST=${MM_HOST}
PORT=${MM_PORT}
DEBUG=false
EOF
    chmod 600 "${APP_DIR}/.env"
else
    echo "Файл ${APP_DIR}/.env уже существует — обновляем сетевые параметры."
    sed -i "s/^HOST=.*/HOST=${MM_HOST}/" "${APP_DIR}/.env"
    sed -i "s/^SESSION_COOKIE_SECURE=.*/SESSION_COOKIE_SECURE=${COOKIE_SECURE}/" "${APP_DIR}/.env"
fi

chown -R "${MM_USER}:${MEDIA_GROUP}" "${APP_DIR}"
chmod 750 "${APP_DIR}"
chmod 770 "${APP_DIR}/tmp_downloads"

# Systemd unit для Music Manager
cat > /etc/systemd/system/music-manager.service <<EOF
[Unit]
Description=Music Downloader & Tag Manager Service
After=network.target
AssertPathExists=${APP_DIR}

[Service]
Type=simple
User=${MM_USER}
Group=${MEDIA_GROUP}
UMask=0002
WorkingDirectory=${APP_DIR}
EnvironmentFile=${APP_DIR}/.env
ExecStart=${APP_DIR}/venv/bin/uvicorn app.main:app --host ${MM_HOST} --port ${MM_PORT}
Restart=on-failure
RestartSec=5s

# Security hardening
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=read-only
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictRealtime=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
ReadWritePaths=${APP_DIR} ${MUSIC_DIR}

[Install]
WantedBy=multi-user.target
EOF

# ------------------------------------------------------------------------------
# 6. Развертывание Navidrome (Binary + Systemd)
# ------------------------------------------------------------------------------
echo -e "\n${YELLOW}[5/8] Установка и настройка Navidrome v${NAVIDROME_VERSION}...${NC}"

ARCH="$(uname -m)"
case "${ARCH}" in
    x86_64)  ARCH_SUFFIX="linux_amd64" ;;
    aarch64) ARCH_SUFFIX="linux_arm64" ;;
    armv7l)  ARCH_SUFFIX="linux_armv7" ;;
    *)
        echo -e "${RED}[ERROR] Неподдерживаемая архитектура: ${ARCH}${NC}" >&2
        exit 1
        ;;
esac

mkdir -p "${NAVIDROME_DIR}" "${NAVIDROME_DATA_DIR}/data"

ND_TARBALL="navidrome_${NAVIDROME_VERSION}_${ARCH_SUFFIX}.tar.gz"
ND_URL="https://github.com/navidrome/navidrome/releases/download/v${NAVIDROME_VERSION}/${ND_TARBALL}"

echo "Загрузка Navidrome (${ND_URL})..."
curl -fsSL "${ND_URL}" -o "/tmp/${ND_TARBALL}"
tar -xzf "/tmp/${ND_TARBALL}" -C "${NAVIDROME_DIR}" navidrome
rm -f "/tmp/${ND_TARBALL}"
chmod 755 "${NAVIDROME_DIR}/navidrome"

# Конфигурация Navidrome (navidrome.toml)
cat > "${NAVIDROME_DATA_DIR}/navidrome.toml" <<EOF
# ==============================================================================
# Navidrome Music Server Configuration
# ==============================================================================
MusicFolder = "${MUSIC_DIR}"
DataFolder = "${NAVIDROME_DATA_DIR}/data"
LogLevel = "INFO"
Address = "${ND_HOST}"
Port = ${ND_PORT}
ScanSchedule = "@every 1m"
SessionTimeout = "24h"
EOF

chown -R "${ND_USER}:${MEDIA_GROUP}" "${NAVIDROME_DATA_DIR}"
chmod 750 "${NAVIDROME_DATA_DIR}"
chmod 640 "${NAVIDROME_DATA_DIR}/navidrome.toml"

# Systemd unit для Navidrome
cat > /etc/systemd/system/navidrome.service <<EOF
[Unit]
Description=Navidrome Music Server and Streamer
After=remote-fs.target network.target
AssertPathExists=${NAVIDROME_DATA_DIR}

[Service]
Type=simple
User=${ND_USER}
Group=${MEDIA_GROUP}
UMask=0002
WorkingDirectory=${NAVIDROME_DATA_DIR}
ExecStart=${NAVIDROME_DIR}/navidrome --configfile ${NAVIDROME_DATA_DIR}/navidrome.toml
Restart=on-failure
RestartSec=5s

# Security hardening
ProtectSystem=full
ProtectHome=yes
NoNewPrivileges=yes
PrivateTmp=yes
ReadWritePaths=${NAVIDROME_DATA_DIR} ${MUSIC_DIR}

[Install]
WantedBy=multi-user.target
EOF

# ------------------------------------------------------------------------------
# 7. Настройка Nginx Reverse Proxy и SSL (Опционально)
# ------------------------------------------------------------------------------
if [[ "${ENABLE_NGINX}" == "true" && -n "${DOMAIN_MM}" && -n "${DOMAIN_ND}" ]]; then
    echo -e "\n${YELLOW}[6/8] Настройка Nginx reverse proxy для ${DOMAIN_MM} и ${DOMAIN_ND}...${NC}"

    # Конфигурация Nginx для Music Manager
    cat > "/etc/nginx/sites-available/music-manager.conf" <<EOF
server {
    listen 80;
    server_name ${DOMAIN_MM};

    client_max_body_size 128M;

    location / {
        proxy_pass http://127.0.0.1:${MM_PORT};
        proxy_http_version 1.1;

        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header X-Forwarded-Host \$host;
        proxy_set_header X-Forwarded-Port \$server_port;

        # WebSocket & SSE streaming support
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";

        proxy_buffering off;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;
    }
}
EOF

    # Конфигурация Nginx для Navidrome
    cat > "/etc/nginx/sites-available/navidrome.conf" <<EOF
server {
    listen 80;
    server_name ${DOMAIN_ND};

    client_max_body_size 100M;

    location / {
        proxy_pass http://127.0.0.1:${ND_PORT};
        proxy_http_version 1.1;

        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header X-Forwarded-Host \$host;
        proxy_set_header X-Forwarded-Port \$server_port;

        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";

        # Оптимизация под непрерывный аудиостриминг
        proxy_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
EOF

    ln -sf "/etc/nginx/sites-available/music-manager.conf" "/etc/nginx/sites-enabled/"
    ln -sf "/etc/nginx/sites-available/navidrome.conf" "/etc/nginx/sites-enabled/"
    rm -f /etc/nginx/sites-enabled/default

    nginx -t
    systemctl reload nginx

    # Выпуск сертификатов Let's Encrypt, если указан email
    if [[ -n "${CERTBOT_EMAIL}" ]]; then
        echo "Получение SSL-сертификатов через Certbot..."
        certbot --nginx --non-interactive --agree-tos -m "${CERTBOT_EMAIL}" \
            -d "${DOMAIN_MM}" --redirect
        certbot --nginx --non-interactive --agree-tos -m "${CERTBOT_EMAIL}" \
            -d "${DOMAIN_ND}" --redirect
    else
        echo -e "${YELLOW}[!] Email не указан. Запустите 'certbot --nginx -d ${DOMAIN_MM} -d ${DOMAIN_ND}' вручную для выпуска SSL.${NC}"
    fi
else
    echo -e "\n${YELLOW}[6/8] Шаг Nginx пропущен (ENABLE_NGINX=${ENABLE_NGINX}). Сервисы слушают напрямую.${NC}"
fi

# ------------------------------------------------------------------------------
# 8. Запуск и активация служб
# ------------------------------------------------------------------------------
echo -e "\n${YELLOW}[7/8] Активация и запуск systemd служб...${NC}"
systemctl daemon-reload
systemctl enable --now music-manager
systemctl enable --now navidrome

# ------------------------------------------------------------------------------
# 9. Проверка статуса
# ------------------------------------------------------------------------------
echo -e "\n${YELLOW}[8/8] Проверка статуса сервисов...${NC}"
sleep 2

MM_STATUS="$(systemctl is-active music-manager || true)"
ND_STATUS="$(systemctl is-active navidrome || true)"

if [[ "${MM_STATUS}" == "active" ]]; then
    echo -e "Music Manager:  ${GREEN}[ACTIVE / RUNNING]${NC}"
else
    echo -e "Music Manager:  ${RED}[FAILED (${MM_STATUS})]${NC}"
    journalctl -u music-manager -n 15 --no-pager
fi

if [[ "${ND_STATUS}" == "active" ]]; then
    echo -e "Navidrome:      ${GREEN}[ACTIVE / RUNNING]${NC}"
else
    echo -e "Navidrome:      ${RED}[FAILED (${ND_STATUS})]${NC}"
    journalctl -u navidrome -n 15 --no-pager
fi

# Получаем внешний IP сервера (или домены)
SERVER_IP="$(hostname -I | awk '{print $1}')"

echo -e "\n${GREEN}================================================================${NC}"
echo -e "${GREEN}    Установка успешно завершена!                                ${NC}"
echo -e "${GREEN}================================================================${NC}"
echo -e "Музыкальная библиотека:   ${MUSIC_DIR}"
echo -e ""

if [[ "${ENABLE_NGINX}" == "true" && -n "${DOMAIN_MM}" && -n "${DOMAIN_ND}" ]]; then
    echo -e "1. ${BLUE}Music Manager (HTTPS):${NC}"
    echo -e "   URL:      https://${DOMAIN_MM}"
    echo -e "   Логин:    ${ADMIN_LOGIN}"
    echo -e "   Пароль:   ${YELLOW}${ADMIN_PASSWORD}${NC}"
    echo -e ""
    echo -e "2. ${BLUE}Navidrome (HTTPS):${NC}"
    echo -e "   URL:      https://${DOMAIN_ND}"
    echo -e "   Первый вход: перейдите по ссылке и создайте аккаунт администратора."
else
    echo -e "1. ${BLUE}Music Manager (Прямой порт):${NC}"
    echo -e "   URL:      http://${SERVER_IP}:${MM_PORT}"
    echo -e "   Логин:    ${ADMIN_LOGIN}"
    echo -e "   Пароль:   ${YELLOW}${ADMIN_PASSWORD}${NC}"
    echo -e ""
    echo -e "2. ${BLUE}Navidrome (Прямой порт):${NC}"
    echo -e "   URL:      http://${SERVER_IP}:${ND_PORT}"
    echo -e "   Первый вход: перейдите по ссылке и создайте аккаунт администратора."
fi

echo -e ""
echo -e "Полезные команды управления:"
echo -e "   Логи Music Manager:  journalctl -u music-manager -f"
echo -e "   Логи Navidrome:      journalctl -u navidrome -f"
if [[ "${ENABLE_NGINX}" == "true" ]]; then
    echo -e "   Логи Nginx ошибки:   tail -f /var/log/nginx/error.log"
    echo -e "   Автопродление SSL:   certbot renew --dry-run"
fi
echo -e "   Перезапуск сервисов: systemctl restart music-manager navidrome"
echo -e "${GREEN}================================================================${NC}"
