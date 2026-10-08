# SPDX-License-Identifier: Apache-2.0
#
# topologyd's phase-1 image (topology-l1 §2): the join/rejoin service
# the pull-join hosts talk to. CLI-1 (#108) retired the bundle's admin
# ctl binary — the admin face ships in the cli component's `looming`
# binary — and extracted the module's dependencies into platform/go, so the build
# context is the bundle root (the render contract builds this dockerfile
# with context "."), workspace mode is off, and the topology module's
# replace directive pins platform/go to the sibling directory copied
# below. The docker-cli runtime base was the retired ctl's need (apply's
# docker/ssh channels); topologyd is an HTTP server, so the runtime
# shape mirrors gateway/identity: distroless nonroot.
FROM golang:1.25 AS build
WORKDIR /src/topology
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} GOWORK=off
COPY topology/go.mod topology/go.sum ./
COPY platform/go/go.mod platform/go/go.sum /src/platform/go/
RUN go mod download
COPY topology/ ./
COPY platform/go/ /src/platform/go/
RUN CGO_ENABLED=0 go build -trimpath -o /out/topologyd ./cmd/topologyd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/topologyd /usr/local/bin/topologyd
USER nonroot
ENTRYPOINT ["/usr/local/bin/topologyd"]
