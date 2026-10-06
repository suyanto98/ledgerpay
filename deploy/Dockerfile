# syntax=docker/dockerfile:1
# Multi-stage: compile di image Go, jalankan di distroless (tanpa shell, tanpa package manager,
# user non-root). Permukaan serangan kecil (C-28). Satu binary untuk api/worker/migrate/healthcheck.
# Target `mock-provider` (dev/test/demo saja) adalah image TERPISAH: binary mock tidak pernah ikut
# di image ledgerpay yang dideploy.

FROM golang:1.26.6-alpine AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
# BUILD_TAGS=demo → image demo (endpoint /api/v1/demo/* + titik fault + migrasi demo, O15). Image
# release dibangun TANPA tag: kode demo tidak ada di binary-nya (TestBuild_DemoEndpointsAbsentWithoutDemoMode).
ARG BUILD_TAGS=""
# Tanpa `go mod download`: perintah itu mengunduh SEMUA modul di go.mod, termasuk dependency
# khusus test (testcontainers, Docker SDK) yang besar. `go build` hanya mengunduh yang dipakai binary.
# Cache mount menyimpan modul dan hasil kompilasi antar-build (rebuild jauh lebih cepat).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -tags "${BUILD_TAGS}" -ldflags "-s -w" -o /out/ ./cmd/ledgerpay ./cmd/mock-provider

# ---- payment provider tiruan (ADR-L05), hanya untuk compose dev/demo dan E2E
FROM gcr.io/distroless/static-debian12:nonroot AS mock-provider
COPY --from=build /out/mock-provider /app/mock-provider
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/app/mock-provider"]

# ---- image aplikasi (target default: tahap terakhir)
FROM gcr.io/distroless/static-debian12:nonroot AS ledgerpay
ARG VERSION=dev
LABEL org.opencontainers.image.title="ledgerpay" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/ledgerpay /app/ledgerpay
USER nonroot:nonroot
ENV APP_VERSION=${VERSION}
EXPOSE 8080 9090
# distroless tidak punya curl: healthcheck memakai subcommand binary itu sendiri.
HEALTHCHECK --interval=5s --timeout=3s --start-period=10s --retries=12 CMD ["/app/ledgerpay", "healthcheck"]
ENTRYPOINT ["/app/ledgerpay"]
CMD ["api"]
