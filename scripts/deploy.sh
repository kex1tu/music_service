#!/usr/bin/env bash
# ==============================================================================
# deploy.sh — Скрипт быстрого деплоя Music Manager на боевые серверы
# Использование:
#   ./scripts/deploy.sh [all|nl_music|server2]
# ==============================================================================

set -euo pipefail

TARGET="${1:-all}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_PATH="$ROOT_DIR/bin/music-manager"

# Выбор SSH ключа: выделенный deploy key или мастер-ключ
SSH_KEY=""
if [ -f "$HOME/.ssh/music_deploy_key" ]; then
    SSH_KEY="$HOME/.ssh/music_deploy_key"
elif [ -f "$HOME/.ssh/id_ed25519" ]; then
    SSH_KEY="$HOME/.ssh/id_ed25519"
fi

SSH_OPTS=("-o" "StrictHostKeyChecking=accept-new" "-o" "ConnectTimeout=10")
if [ -n "$SSH_KEY" ]; then
    SSH_OPTS+=("-i" "$SSH_KEY")
fi

echo "=========================================================="
echo "🚀 Music Manager Deployment Pipeline"
echo "Target: $TARGET"
echo "Working directory: $ROOT_DIR"
echo "=========================================================="

# 1. Прогон модульных тестов
echo "🧪 [1/4] Запуск локальных тестов..."
(cd "$ROOT_DIR" && go test ./...)
echo "✅ Тесты пройдены успешно."

# 2. Кросс-компиляция статического бинарника
echo "🔨 [2/4] Компиляция чистого статического бинарника Pure Go..."
mkdir -p "$ROOT_DIR/bin"
(cd "$ROOT_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$BIN_PATH" cmd/server/main.go)
chmod +x "$BIN_PATH"
BIN_SIZE=$(du -h "$BIN_PATH" | cut -f1)
echo "✅ Бинарник собран: $BIN_PATH ($BIN_SIZE)"

# 3. Функция раскатки на сервер
deploy_to_server() {
    local ID="$1"
    local NAME="$2"
    local HOST="$3"
    local PORT="$4"
    local USER="$5"
    local APP_DIR="$6"
    local APP_USER="$7"
    local APP_GROUP="$8"
    local SERVICE="$9"
    local HEALTH_URL="${10}"

    echo "----------------------------------------------------------"
    echo "📦 Раскатка на $NAME ($HOST:$PORT)..."
    echo "----------------------------------------------------------"

    # 3.1. Синхронизация репозитория Git
    echo "  → Актуализация Git репозитория на сервере..."
    ssh "${SSH_OPTS[@]}" -p "$PORT" "$USER@$HOST" \
        "cd '$APP_DIR' && git fetch origin main && git checkout -- . && git pull --ff-only origin main"

    # 3.2. Загрузка бинарника
    echo "  → Загрузка скомпилированного бинарника в $APP_DIR/bin/music-manager.new..."
    ssh "${SSH_OPTS[@]}" -p "$PORT" "$USER@$HOST" "mkdir -p '$APP_DIR/bin'"
    scp "${SSH_OPTS[@]}" -P "$PORT" "$BIN_PATH" "$USER@$HOST:$APP_DIR/bin/music-manager.new"

    # 3.3. Атомарная замена и перезапуск службы
    echo "  → Атомарное обновление и перезапуск службы $SERVICE..."
    ssh "${SSH_OPTS[@]}" -p "$PORT" "$USER@$HOST" bash -s << EOF
        set -e
        chmod 755 '$APP_DIR/bin/music-manager.new'
        chown '$APP_USER:$APP_GROUP' '$APP_DIR/bin/music-manager.new'

        if [ -f '$APP_DIR/bin/music-manager' ]; then
            cp -f '$APP_DIR/bin/music-manager' '$APP_DIR/bin/music-manager.bak'
        fi

        mv -f '$APP_DIR/bin/music-manager.new' '$APP_DIR/bin/music-manager'
        systemctl restart '$SERVICE'
EOF

    # 3.4. Health Check с автоматическим откатом
    echo "  → Проверка здоровья сервиса: $HEALTH_URL..."
    local HEALTHY=0
    for i in {1..15}; do
        local STATUS
        STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$HEALTH_URL" || true)
        if [ "$STATUS" = "200" ]; then
            echo "  ✅ Health check пройден успешно (HTTP 200, попытка $i)!"
            HEALTHY=1
            break
        fi
        echo "  ... попытка $i/15: код $STATUS, пауза 2 сек..."
        sleep 2
    done

    if [ "$HEALTHY" -ne 1 ]; then
        echo "  ❌ ОШИБКА: Health check не пройден! Выполняю автоматический откат..."
        ssh "${SSH_OPTS[@]}" -p "$PORT" "$USER@$HOST" bash -s << EOF
            if [ -f '$APP_DIR/bin/music-manager.bak' ]; then
                cp -f '$APP_DIR/bin/music-manager.bak' '$APP_DIR/bin/music-manager'
                systemctl restart '$SERVICE'
                echo 'Откат на резервную копию выполнен.'
            fi
EOF
        return 1
    fi

    echo "🎉 $NAME успешно обновлен и работает в штатном режиме!"
}

# 4. Выполнение раскатки согласно $TARGET
if [ "$TARGET" = "all" ] || [ "$TARGET" = "nl_music" ]; then
    deploy_to_server \
        "nl_music" \
        "Сервер 1 (nl_music)" \
        "194.76.168.20" \
        "56924" \
        "root" \
        "/opt/music-manager" \
        "music" \
        "media" \
        "music-manager.service" \
        "https://musicdownloader.verymad.net/api/health"
fi

if [ "$TARGET" = "all" ] || [ "$TARGET" = "server2" ]; then
    deploy_to_server \
        "server2" \
        "Сервер 2 (mymusicdownload)" \
        "13.143.193.207" \
        "56924" \
        "root" \
        "/root/music_service" \
        "root" \
        "root" \
        "music-manager.service" \
        "https://mymusicdownload.verymad.net/api/health"
fi

echo "=========================================================="
echo "✨ Деплой завершен на всех запрошенных серверах!"
echo "=========================================================="
