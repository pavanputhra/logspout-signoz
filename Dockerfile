# Builds a logspout binary with the SigNoz adapter compiled in.
#
# The adapter source is copied from the build context and wired in with a
# `replace` directive, so the image always contains exactly the code in this
# checkout. The v1 image inherited gliderlabs/logspout's ONBUILD triggers, which
# resolved this module through the Go module proxy instead — the proxy served a
# cached view of the branch, so a fresh commit could be built with stale code.
# That is what versoin_bump.md existed to work around.

ARG GO_VERSION=1.22
ARG LOGSPOUT_VERSION=v3.2.14
ARG ALPINE_VERSION=3.20

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

RUN apk add --no-cache git

ARG LOGSPOUT_VERSION
RUN git clone --depth 1 --branch ${LOGSPOUT_VERSION} \
    https://github.com/gliderlabs/logspout.git /src/logspout

# Only what the build needs, so editing the README does not bust the cache.
COPY go.mod go.sum /src/logspout-signoz/
COPY signoz /src/logspout-signoz/signoz

# Replaces logspout's own module list with ours.
COPY custom/modules.go /src/logspout/modules.go

WORKDIR /src/logspout
RUN go mod edit -go=${GO_VERSION} \
      -require=github.com/pavanputhra/logspout-signoz/v2@v2.0.0 \
      -replace=github.com/pavanputhra/logspout-signoz/v2=/src/logspout-signoz \
 && go mod tidy

# Cross-compiled from the build platform: multi-arch images build at native
# speed instead of under QEMU emulation.
ARG TARGETOS
ARG TARGETARCH
ARG LOGSPOUT_VERSION
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.Version=${LOGSPOUT_VERSION}-signoz" -o /bin/logspout

# Assemble the runtime overlay here, on the build platform, so the final stage
# needs no RUN at all — a target-platform RUN would drag every arm64 build
# through QEMU emulation in CI.
# /var/run is a symlink to /run on Alpine, so the overlay targets /run.
RUN mkdir -p /out/run \
 && ln -s /tmp/docker.sock /out/run/docker.sock

FROM alpine:${ALPINE_VERSION}

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/ /
COPY --from=build /bin/logspout /bin/logspout

VOLUME /mnt/routes
EXPOSE 80
ENTRYPOINT ["/bin/logspout"]
