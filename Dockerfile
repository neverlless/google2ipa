# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/google2ipa ./cmd/google2ipa

FROM gcr.io/distroless/static-debian13:nonroot
LABEL org.opencontainers.image.source="https://github.com/neverlless/google2ipa" \
      org.opencontainers.image.description="Sync Google Workspace users into FreeIPA / Red Hat IdM" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/google2ipa /google2ipa
ENTRYPOINT ["/google2ipa"]
CMD ["--config", "/etc/google2ipa/config.yaml"]
