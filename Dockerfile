# syntax=docker/dockerfile:1.7
#
# gatr — single-binary, distroless, ~12 MB. The CLI stands alone: no
# server, no database, no gatr cloud account required.
#
# Build:
#   docker build -t gatr-cli .
#
# Run (against a gatr.yaml in the host CWD):
#   docker run --rm -v $PWD:/work gatr-cli validate
#   docker run --rm -e STRIPE_SECRET_KEY=sk_test_... -v $PWD:/work \
#     gatr-cli push --dry-run
#
# Build-args:
#   VERSION  — embedded into the binary via -ldflags (default 0.0.0-docker).

FROM golang:1.25-alpine AS build
WORKDIR /src

# CGO disabled keeps the binary statically linked → distroless/static can run it.
# GOWORK=off pins each module to its own go.mod during the build; go.work is a
# local-dev convenience, not a build input.
ENV CGO_ENABLED=0 GOWORK=off

COPY pkg/schema/ pkg/schema/
COPY pkg/stripe/ pkg/stripe/
COPY cmd/cli/ cmd/cli/

WORKDIR /src/cmd/cli

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

ARG VERSION=0.0.0-docker
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath \
      -ldflags="-s -w -X main.Version=${VERSION}" \
      -o /out/gatr .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gatr /usr/local/bin/gatr
# /work is the conventional mount point for the user's gatr.yaml.
WORKDIR /work
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/gatr"]
