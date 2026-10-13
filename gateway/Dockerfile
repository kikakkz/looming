# SPDX-License-Identifier: Apache-2.0
#
# gateway's phase-1 image (topology-l1 §2: every component is a
# container on the single runtime plane). #171's buildinfo made gateway
# a platform/go consumer, so — like topologyd — the build context is
# the bundle root (the render contract builds this dockerfile with
# context "."), workspace mode is off, and the module's replace
# directive pins platform/go to the sibling directory copied below.
# GOPROXY is a build-arg so mirrored/offline environments override it
# without editing the file. Distroless nonroot: no shell, no package
# manager; the static variant carries CA certificates for the TLS era
# (#110) while phase-1 HTTP stays plain.

FROM golang:1.25 AS build
WORKDIR /src/gateway
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} GOWORK=off
COPY gateway/go.mod gateway/go.sum ./
COPY platform/go/go.mod platform/go/go.sum /src/platform/go/
RUN go mod download
COPY gateway/ ./
COPY platform/go/ /src/platform/go/
RUN CGO_ENABLED=0 go build -trimpath -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /usr/local/bin/gateway
USER nonroot
ENTRYPOINT ["/usr/local/bin/gateway"]
