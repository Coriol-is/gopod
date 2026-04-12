# Multi-stage build for the picoclaw binary.
# The final image is scratch-like (distroless) — picoclaw is a single
# static Go binary with no runtime dependencies.

# --- Build stage ---
FROM golang:1.24-alpine AS build

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags="-X main.version=${VERSION}" \
    -o /picoclaw \
    ./cmd/picoclaw

# --- Runtime stage ---
FROM alpine:3.21

# ca-certificates for HTTPS (Telegram API, OpenAI API).
# docker-cli for picoclaw to manage agent containers via the
# mounted docker.sock. picoclaw uses the Go Docker SDK, but
# the CLI is useful for debugging inside the compose container.
RUN apk add --no-cache ca-certificates docker-cli

COPY --from=build /picoclaw /usr/local/bin/picoclaw

# picoclaw reads .env from the working directory.
WORKDIR /app

ENTRYPOINT ["picoclaw"]
