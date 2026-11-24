# syntax=docker/dockerfile:1
# cactusTrade: one distroless, non-root image serving the API and the web app.
# Build stages run everything in containers, so `docker build` needs nothing on the host.

FROM node:22-alpine@sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a AS web
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@9.15.9 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26.11-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS build
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

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/cactustrade /cactustrade
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV CACTUSTRADE_CONTAINER=1 CACTUSTRADE_DATA_DIR=/data
EXPOSE 8443
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s CMD ["/cactustrade", "healthcheck"]
ENTRYPOINT ["/cactustrade"]
CMD ["serve"]
