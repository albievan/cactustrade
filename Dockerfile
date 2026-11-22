# syntax=docker/dockerfile:1
# cactusTrade: one distroless, non-root image serving the API and the web app.
# Build stages run everything in containers, so `docker build` needs nothing on the host.

FROM node:20-alpine AS web
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@9.15.9 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26.11-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG BUILD=0
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-s -w -X main.version=${VERSION} -X main.build=${BUILD}" \
      -o /out/cactustrade ./cmd/cactustrade \
 && mkdir -p /out/data

# `make build-in-container` exports the binary from here (any GOOS/GOARCH).
FROM scratch AS export
COPY --from=build /out/cactustrade /

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cactustrade /cactustrade
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV CACTUSTRADE_CONTAINER=1 CACTUSTRADE_DATA_DIR=/data
EXPOSE 8443
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s CMD ["/cactustrade", "healthcheck"]
ENTRYPOINT ["/cactustrade"]
CMD ["serve"]
