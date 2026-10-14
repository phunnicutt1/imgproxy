# syntax=docker/dockerfile:1
# MinIO server + mc client built from source at pinned release tags.
#
# Why: Docker Hub withdrew minio/minio and minio/mc (both repositories return 404
# since 2026), so `minio/minio:latest` / `minio/mc:latest` only work where an old pull
# is still cached. These pinned releases match the last published images.
#
# Targets: `minio` (server, has curl for the compose healthcheck) and `mc`
# (client + /bin/sh for the `seed` service in docker/dev/compose.yml).
#
# MinIO and mc are AGPL-3.0: build for local dev use only. Never push these
# images to a public registry.

ARG GO_IMAGE=golang:1.24.6-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim

FROM ${GO_IMAGE} AS build-minio
ARG MINIO_RELEASE_TAG=RELEASE.2025-09-07T16-13-09Z
WORKDIR /src
RUN git clone --quiet --depth 1 --branch "${MINIO_RELEASE_TAG}" https://github.com/minio/minio.git .
# Same ldflags as upstream's release build: gen-ldflags.go takes the release time
# (RELEASE.2025-09-07T16-13-09Z -> 2025-09-07T16:13:09Z) and MINIO_RELEASE=RELEASE.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    v="$(echo "${MINIO_RELEASE_TAG#RELEASE.}" | sed -E 's/T([0-9]{2})-([0-9]{2})-([0-9]{2})Z$/T\1:\2:\3Z/')" \
    && CGO_ENABLED=0 MINIO_RELEASE=RELEASE go build -trimpath -tags kqueue \
         -ldflags "$(MINIO_RELEASE=RELEASE go run buildscripts/gen-ldflags.go "$v")" \
         -o /out/minio .

FROM ${GO_IMAGE} AS build-mc
ARG MC_RELEASE_TAG=RELEASE.2025-08-13T08-35-41Z
WORKDIR /src
RUN git clone --quiet --depth 1 --branch "${MC_RELEASE_TAG}" https://github.com/minio/mc.git .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    v="$(echo "${MC_RELEASE_TAG#RELEASE.}" | sed -E 's/T([0-9]{2})-([0-9]{2})-([0-9]{2})Z$/T\1:\2:\3Z/')" \
    && CGO_ENABLED=0 MC_RELEASE=RELEASE go build -trimpath -tags kqueue \
         -ldflags "$(MC_RELEASE=RELEASE go run buildscripts/gen-ldflags.go "$v")" \
         -o /out/mc .

FROM ${RUNTIME_IMAGE} AS runtime-base
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

FROM runtime-base AS mc
COPY --from=build-mc /out/mc /usr/bin/mc
ENTRYPOINT ["mc"]

FROM runtime-base AS minio
COPY --from=build-minio /out/minio /usr/bin/minio
COPY --from=build-mc /out/mc /usr/bin/mc
EXPOSE 9000 9001
VOLUME ["/data"]
ENTRYPOINT ["minio"]
CMD ["server", "/data"]
