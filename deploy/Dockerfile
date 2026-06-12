# Multi-stage build. Output is a static bbg binary that owns the world.
# go directive bumped to 1.25 by x/image transitive deps; pin the toolchain
# to match.
FROM golang:1.25-alpine AS build
WORKDIR /src

RUN apk add --no-cache git ca-certificates tzdata

COPY go.mod go.sum* ./
RUN go mod download || true

COPY . .

ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/bbg ./cmd/server \
 && go build -trimpath -ldflags="-s -w" -o /out/bbg-puzzle-build ./cmd/puzzle-build \
 && go build -trimpath -ldflags="-s -w" -o /out/bbg-bot-candidates ./cmd/bot-candidates \
 && go build -trimpath -ldflags="-s -w" -o /out/bbg-og-render ./cmd/og-render \
 && go build -trimpath -ldflags="-s -w" -o /out/bbg-admin ./cmd/admin

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata postgresql16-client
WORKDIR /app

COPY --from=build /out/ /usr/local/bin/
COPY --from=build /src/web/ /app/web/
COPY --from=build /src/db/ /app/db/

ENV BBG_WEB_DIR=/app/web
EXPOSE 8080
USER 65532:65532

CMD ["bbg"]
