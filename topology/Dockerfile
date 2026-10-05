# SPDX-License-Identifier: Apache-2.0
#
# looming-ctl's phase-1 image (topology-l1 §2). The ctl's cmd placement
# under topology/ is a T1 temporary pending #108's CLI layout decision.
#
# The runtime base is docker:27-cli — NOT the distroless variant the
# other components use: apply converges by shelling out to the docker
# CLI (with the compose plugin) and needs an ssh client for the
# DOCKER_HOST=ssh:// executor channel, and the operator's docker socket
# must be mounted at run time (group membership governs access). A
# distroless runtime would carry neither binary and could not run a
# single non-dry-run apply. gateway/identity stay distroless: their
# binaries speak HTTP, never exec.

FROM golang:1.25 AS build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/looming-ctl ./cmd/looming-ctl

FROM docker:27-cli
RUN apk add --no-cache openssh-client
COPY --from=build /out/looming-ctl /usr/local/bin/looming-ctl
ENTRYPOINT ["/usr/local/bin/looming-ctl"]
