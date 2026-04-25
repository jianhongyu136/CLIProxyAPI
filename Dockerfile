FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./pa ./cmd/server/

FROM alpine:3.22.0

RUN apk add --no-cache nginx gettext tzdata \
    && adduser -D -u 1000 appuser \
    && mkdir -p /app/data /app/docker /run/nginx /tmp/nginx /tmp/nginx/client_body /tmp/nginx/proxy /tmp/nginx/fastcgi /tmp/nginx/uwsgi /tmp/nginx/scgi \
    && chown -R appuser:appuser /app /run/nginx /tmp/nginx

WORKDIR /app

COPY --from=builder --chown=appuser:appuser /app/pa ./pa
COPY --chown=appuser:appuser config.example.yaml /app/data/config.yaml
COPY --chown=appuser:appuser docker/entrypoint.sh /app/docker/entrypoint.sh
COPY --chown=appuser:appuser docker/nginx.conf.template /app/docker/nginx.conf.template

ENV TZ=Asia/Shanghai
ENV APP_PORT=8080
ENV NGINX_PORT=7860

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime \
    && echo "${TZ}" > /etc/timezone \
    && chmod +x /app/docker/entrypoint.sh

USER appuser

EXPOSE 7860

CMD ["/app/docker/entrypoint.sh"]
