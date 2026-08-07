FROM node:22-alpine AS frontend
WORKDIR /build
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS backend
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG VERSION=dev
ARG COMMIT=dev
# Release builds pass VERSION/COMMIT explicitly; local builds fall back to
# git describe when the checkout is available in the build context.
RUN if [ "$VERSION" = "dev" ] && [ -d .git ]; then \
      VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"; \
      COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo dev)"; \
    fi; \
    CGO_ENABLED=0 go build -ldflags \
      "-X github.com/calvertjadon/docu-kiosk/internal/version.Version=${VERSION} \
       -X github.com/calvertjadon/docu-kiosk/internal/version.Commit=${COMMIT}" \
      -o server ./cmd/server

FROM alpine:3.22
RUN addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=backend /build/server .
COPY --from=frontend /build/dist ./web/dist
COPY sql/migrations/ ./sql/migrations/
RUN mkdir /app/data && chown app:app /app/data
USER app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8080/ || exit 1
CMD ["./server"]
