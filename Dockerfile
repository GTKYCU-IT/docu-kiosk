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
RUN CGO_ENABLED=0 go build -o server ./cmd/server

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
