# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/coding-plan-usage ./cmd/coding-plan-usage

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && mkdir -p /config/data \
    && chown -R app:app /config

WORKDIR /app
COPY --from=build /out/coding-plan-usage /usr/local/bin/coding-plan-usage
USER app:app

ENTRYPOINT ["coding-plan-usage"]
CMD ["run", "--config", "/config/config.yaml"]
