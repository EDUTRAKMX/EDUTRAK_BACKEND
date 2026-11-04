# EDUTRAK backend image: one image with the api, migrate and seed binaries.
# Build:  docker build -t edutrak-backend .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seed ./cmd/seed

FROM alpine:3.20
RUN adduser -D -u 10001 edutrak && mkdir -p /var/lib/edutrak/evidence && chown edutrak /var/lib/edutrak/evidence && chmod 700 /var/lib/edutrak/evidence
COPY --from=build /out/ /usr/local/bin/
USER edutrak
# Evidence lives on a volume mounted here (STORAGE_DIR); never inside the image.
VOLUME ["/var/lib/edutrak/evidence"]
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=5 \
  CMD wget -qO- "http://127.0.0.1:${HTTP_PORT:-8080}/healthz" >/dev/null || exit 1
CMD ["api"]
