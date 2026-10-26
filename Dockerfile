# Multi-stage build for the gopod binary.
# The final image is scratch-like (distroless) — gopod is a single
# static Go binary with no runtime dependencies.

# --- Build stage ---
FROM golang:1.26-alpine AS build

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags="-X main.version=${VERSION}" \
    -o /gopod \
    ./cmd/gopod
RUN CGO_ENABLED=0 go build -o /gopod-group-relay ./cmd/gopod-group-relay
RUN CGO_ENABLED=0 go build -o /gopod-group-canary ./cmd/gopod-group-canary
RUN CGO_ENABLED=0 go build -o /gopod-group-admin ./cmd/gopod-group-admin

# --- Runtime stage ---
FROM alpine:3.21

# ca-certificates for HTTPS (Telegram API, OpenAI API).
# docker-cli for gopod to manage agent containers via the
# mounted docker.sock. gopod uses the Go Docker SDK, but
# the CLI is useful for debugging inside the compose container.
RUN apk add --no-cache ca-certificates docker-cli

COPY --from=build /gopod /usr/local/bin/gopod
COPY --from=build /gopod-group-relay /usr/local/bin/gopod-group-relay
COPY --from=build /gopod-group-canary /usr/local/bin/gopod-group-canary
COPY --from=build /gopod-group-admin /usr/local/bin/gopod-group-admin

# gopod reads .env from the working directory.
WORKDIR /app

ENTRYPOINT ["gopod"]
