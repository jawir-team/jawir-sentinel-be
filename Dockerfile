# syntax=docker/dockerfile:1

FROM golang:1.26.0-alpine3.23 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/sentinel-api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/sentinel-worker ./cmd/worker

FROM alpine:3.23.6 AS runtime

RUN apk add --no-cache ca-certificates && \
    addgroup -S -g 65532 sentinel && \
    adduser -S -D -H -u 65532 -G sentinel sentinel

WORKDIR /app
COPY --from=builder --chown=sentinel:sentinel /out/sentinel-api /app/sentinel-api
COPY --from=builder --chown=sentinel:sentinel /out/sentinel-worker /app/sentinel-worker

ENV APP_PORT=8080
EXPOSE 8080

USER sentinel:sentinel
CMD ["/app/sentinel-api"]
