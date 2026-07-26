FROM golang:1.25.12-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/northstar-api \
    ./cmd/server

FROM alpine:3.21

RUN apk add --no-cache ca-certificates \
    && addgroup -S northstar \
    && adduser -S -G northstar -H -s /sbin/nologin northstar

COPY --from=build --chown=northstar:northstar /out/northstar-api /usr/local/bin/northstar-api
COPY --from=build --chown=northstar:northstar /src/web /app/web

USER northstar
WORKDIR /app
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/northstar-api"]
