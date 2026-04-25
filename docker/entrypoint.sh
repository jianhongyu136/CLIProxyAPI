#!/bin/sh
set -eu

CONFIG_SOURCE="${CONFIG_PATH:-/app/data/config.yaml}"
CONFIG_RENDERED="/tmp/config.yaml"
NGINX_TEMPLATE="/app/docker/nginx.conf.template"
NGINX_CONF="/tmp/nginx.conf"
APP_BINARY="/app/pa"
APP_HOST="${APP_HOST:-127.0.0.1}"
APP_PORT="${APP_PORT:-8080}"
NGINX_PORT="${NGINX_PORT:-7860}"
NGINX_PROXY_CONNECT_TIMEOUT="${NGINX_PROXY_CONNECT_TIMEOUT:-3600s}"
NGINX_PROXY_SEND_TIMEOUT="${NGINX_PROXY_SEND_TIMEOUT:-3600s}"
NGINX_PROXY_READ_TIMEOUT="${NGINX_PROXY_READ_TIMEOUT:-3600s}"
NGINX_CLIENT_MAX_BODY_SIZE="${NGINX_CLIENT_MAX_BODY_SIZE:-100m}"
FORWARD_AUTH_HEADER="${FORWARD_AUTH_HEADER:-}"

if [ ! -f "$CONFIG_SOURCE" ]; then
  echo "config file not found: $CONFIG_SOURCE" >&2
  exit 1
fi

AUTHORIZATION_HEADER_VALUE='""'
if [ -n "$FORWARD_AUTH_HEADER" ]; then
  header_var_name="$(printf '%s' "$FORWARD_AUTH_HEADER" | tr '[:upper:]' '[:lower:]' | tr '-' '_')"
  AUTHORIZATION_HEADER_VALUE="\$http_${header_var_name}"
fi

awk -v app_host="$APP_HOST" -v app_port="$APP_PORT" '
BEGIN { host_written = 0; port_written = 0 }
/^[[:space:]]*host:[[:space:]]*/ {
  print "host: \"" app_host "\""
  host_written = 1
  next
}
/^[[:space:]]*port:[[:space:]]*/ {
  print "port: " app_port
  port_written = 1
  next
}
{ print }
END {
  if (!host_written) {
    print "host: \"" app_host "\""
  }
  if (!port_written) {
    print "port: " app_port
  }
}
' "$CONFIG_SOURCE" > "$CONFIG_RENDERED"

export APP_HOST APP_PORT NGINX_PORT NGINX_PROXY_CONNECT_TIMEOUT NGINX_PROXY_SEND_TIMEOUT NGINX_PROXY_READ_TIMEOUT NGINX_CLIENT_MAX_BODY_SIZE AUTHORIZATION_HEADER_VALUE
envsubst '${APP_HOST} ${APP_PORT} ${NGINX_PORT} ${NGINX_PROXY_CONNECT_TIMEOUT} ${NGINX_PROXY_SEND_TIMEOUT} ${NGINX_PROXY_READ_TIMEOUT} ${NGINX_CLIENT_MAX_BODY_SIZE} ${AUTHORIZATION_HEADER_VALUE}' < "$NGINX_TEMPLATE" > "$NGINX_CONF"

"$APP_BINARY" --config "$CONFIG_RENDERED" &
APP_PID=$!

cleanup() {
  if kill -0 "$APP_PID" 2>/dev/null; then
    kill "$APP_PID"
    wait "$APP_PID" || true
  fi
}
trap cleanup INT TERM EXIT

nginx -p /tmp/nginx -g 'daemon off;' -c "$NGINX_CONF"
