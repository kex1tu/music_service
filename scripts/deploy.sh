#!/usr/bin/env bash
# Deploy the local build (including embedded UI), preserving remote source trees.
# Usage: ./scripts/deploy.sh all|nl_music|server2
set -euo pipefail
TARGET="${1:-}"
case "$TARGET" in all|nl_music|server2) ;; *) echo 'Usage: ./scripts/deploy.sh all|nl_music|server2' >&2; exit 2 ;; esac
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_PATH="$ROOT_DIR/bin/music-manager"
DEPLOY_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o ServerAliveInterval=10 -o ServerAliveCountMax=3)
if [[ -f "$HOME/.ssh/music_deploy_key" ]]; then
    SSH_OPTS+=(-i "$HOME/.ssh/music_deploy_key")
elif [[ -f "$HOME/.ssh/id_ed25519" ]]; then
    SSH_OPTS+=(-i "$HOME/.ssh/id_ed25519")
fi
select_server() {
    SERVER_ID="$1"
    case "$SERVER_ID" in
        nl_music) SERVER_HOST=194.76.168.20; APP_DIR=/opt/music-manager; PUBLIC_URL=https://musicdownloader.verymad.net ;;
        server2) SERVER_HOST=13.143.193.207; APP_DIR=/root/music_service; PUBLIC_URL=https://mymusicdownload.verymad.net ;;
    esac
}
remote_run() {
    ssh "${SSH_OPTS[@]}" -p 56924 "root@$SERVER_HOST" bash -s -- "$1" "$APP_DIR" "$DEPLOY_ID" "${BIN_SHA:-preflight}" <<'REMOTE'
set -euo pipefail
mode="$1"; app_dir="$2"; deploy_id="$3"; expected_sha="$4"
service=music-manager.service
bin="$app_dir/bin/music-manager"
staged="$bin.new.$deploy_id"
backup="$bin.bak.$deploy_id"
cd "$app_dir"
[[ "$(systemctl show "$service" -p WorkingDirectory --value)" == "$app_dir" ]]
[[ -f "$bin" ]]
runtime_config() {
    local pid key value config_host='' config_port=''
    pid="$(systemctl show "$service" -p MainPID --value)"
    [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
    [[ "$(readlink "/proc/$pid/exe")" == "$bin" ]] || return 1
    # Read only HOST and PORT; never print the environment or secrets.
    while IFS='=' read -r -d '' key value; do
        case "$key" in HOST) config_host="$value" ;; PORT) config_port="$value" ;; esac
    done < "/proc/$pid/environ"
    if [[ -f .env ]]; then
        while IFS='=' read -r key value; do
            value="${value%$'\r'}"; value="${value#\"}"; value="${value%\"}"
            value="${value#\'}"; value="${value%\'}"
            case "$key" in
                HOST) [[ -n "$config_host" ]] || config_host="$value" ;;
                PORT) [[ -n "$config_port" ]] || config_port="$value" ;;
            esac
        done < .env
    fi
    config_host="${config_host:-127.0.0.1}"; config_port="${config_port:-8010}"
    [[ "$config_port" =~ ^[0-9]+$ ]] && (( config_port > 0 && config_port < 65536 )) || return 1
    case "$config_host" in 0.0.0.0) config_host=127.0.0.1 ;; ::) config_host='[::1]' ;; *:*) config_host="[$config_host]" ;; esac
    local_url="http://$config_host:$config_port"
    echo "Service=$service app=$app_dir local=$local_url"
}
wait_for_health() {
    local i
    for i in {1..15}; do
        if systemctl is-active --quiet "$service" && runtime_config && curl --noproxy '*' --fail --silent --show-error --max-time 5 "$local_url/api/health"; then
            echo; return 0
        fi
        sleep 2
    done
    return 1
}
case "$mode" in
    preflight)
        systemctl is-active --quiet "$service"
        runtime_config
        curl --noproxy '*' --fail --silent --show-error --max-time 5 "$local_url/api/health"
        echo
        ;;
    install)
        [[ -f "$staged" ]]
        [[ "$(sha256sum "$staged" | cut -d ' ' -f1)" == "$expected_sha" ]]
        cp -p "$bin" "$backup"
        chmod 755 "$staged"
        chown --reference="$bin" "$staged"
        mv -f "$staged" "$bin"
        systemctl restart "$service"
        wait_for_health
        pid="$(systemctl show "$service" -p MainPID --value)"
        [[ "$(sha256sum "/proc/$pid/exe" | cut -d ' ' -f1)" == "$expected_sha" ]]
        page="$(curl --noproxy '*' --fail --silent --show-error --max-time 10 "$local_url/")"
        for marker in 'Поиск SoundCloud' 'Плейлисты' 'Загрузить ещё'; do [[ "$page" == *"$marker"* ]]; done
        script="$(curl --noproxy '*' --fail --silent --show-error --max-time 10 "$local_url/static/js/downloader/soundcloud_search.js")"
        [[ "$script" == *'/api/downloads/soundcloud/search'* ]]
        echo "Installed SHA256=$expected_sha backup=$backup"
        ;;
    rollback)
        if [[ ! -f "$backup" ]]; then
            echo 'No backup created; binary replacement was not reached.' >&2
            exit 1
        fi
        cp -p "$backup" "$bin.rollback.$deploy_id"
        mv -f "$bin.rollback.$deploy_id" "$bin"
        systemctl restart "$service"
        wait_for_health
        echo "Rollback completed: $backup"
        ;;
    *) exit 2 ;;
esac
REMOTE
}
public_check() {
    local i page script
    for i in {1..15}; do
        echo "Public health attempt $i: $SERVER_ID"
        if curl --fail --silent --show-error --max-time 10 "$PUBLIC_URL/api/health" &&
            page="$(curl --fail --silent --show-error --max-time 10 "$PUBLIC_URL/")" &&
            script="$(curl --fail --silent --show-error --max-time 10 "$PUBLIC_URL/static/js/downloader/soundcloud_search.js?v=$DEPLOY_ID")" &&
            [[ "$page" == *'Поиск SoundCloud'* && "$page" == *'Плейлисты'* && "$page" == *'Загрузить ещё'* && "$script" == *'/api/downloads/soundcloud/search'* ]]; then
            echo; return 0
        fi
        sleep 2
    done
    return 1
}
servers=("$TARGET")
[[ "$TARGET" != all ]] || servers=(nl_music server2)
echo "Deployment=$DEPLOY_ID target=$TARGET (local working tree, remote sources preserved)"
# Verify both targets before changing either installation.
for server in "${servers[@]}"; do
    select_server "$server"
    echo "Preflight: $SERVER_ID"
    remote_run preflight
done
echo 'Running Go tests...'
(cd "$ROOT_DIR" && go test -v ./...)
echo 'Building static Linux amd64 binary...'
mkdir -p "$ROOT_DIR/bin"
(cd "$ROOT_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$BIN_PATH" cmd/server/main.go)
BIN_SHA="$(sha256sum "$BIN_PATH" | cut -d ' ' -f1)"
echo "Build SHA256=$BIN_SHA"
for server in "${servers[@]}"; do
    select_server "$server"
    echo "Deploying: $SERVER_ID"
    scp "${SSH_OPTS[@]}" -P 56924 "$BIN_PATH" "root@$SERVER_HOST:$APP_DIR/bin/music-manager.new.$DEPLOY_ID"
    if ! remote_run install; then
        echo "Installation failed on $SERVER_ID; rolling back and stopping." >&2
        remote_run rollback || { echo "ROLLBACK FAILED on $SERVER_ID; manual attention required." >&2; exit 1; }
        exit 1
    fi
    if ! public_check; then
        echo "Public check failed on $SERVER_ID; rolling back and stopping." >&2
        remote_run rollback || { echo "ROLLBACK FAILED on $SERVER_ID; manual attention required." >&2; exit 1; }
        exit 1
    fi
    echo "SUCCESS: $SERVER_ID $PUBLIC_URL SHA256=$BIN_SHA"
done
echo 'Deployment completed on all requested servers.'
