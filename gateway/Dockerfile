# SPDX-License-Identifier: Apache-2.0
#
# gateway's phase-1 image (topology-l1 §2: every component is a
# container on the single runtime plane). Multi-stage build with the
# component directory as context — compose renders
# build.context: gateway relative to the bundle root `looming apply`
# runs from. GOPROXY is a build-arg so mirrored/offline environments
# override it without editing the file. Distroless nonroot: no shell,
# no package manager; the static variant carries CA certificates for
# the TLS era (#110) while phase-1 HTTP stays plain.

FROM golang:1.25 AS build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /usr/local/bin/gateway
USER nonroot
ENTRYPOINT ["/usr/local/bin/gateway"]
