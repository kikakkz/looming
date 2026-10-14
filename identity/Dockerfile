# SPDX-License-Identifier: Apache-2.0
#
# identityd's phase-1 image (topology-l1 §2). Shape mirrors
# gateway/Dockerfile: #171's buildinfo made identity a platform/go
# consumer, so the build context is the bundle root (the render
# contract builds this dockerfile with context "."), workspace mode is
# off, and the module's replace directive pins platform/go to the
# sibling directory copied below. GOPROXY is a build-arg;
# distroless nonroot runtime.

FROM golang:1.25 AS build
WORKDIR /src/identity
ARG GOPROXY=https://proxy.golang.org,direct
# AD-40 (S2 #172): release CI passes the component VERSION file content;
# S3 (#168) wires it into buildinfo ldflags + the OCI image label.
ARG VERSION
ENV GOPROXY=${GOPROXY} GOWORK=off
COPY identity/go.mod identity/go.sum ./
COPY platform/go/go.mod platform/go/go.sum /src/platform/go/
RUN go mod download
COPY identity/ ./
COPY platform/go/ /src/platform/go/
RUN CGO_ENABLED=0 go build -trimpath -o /out/identityd ./cmd/identityd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/identityd /usr/local/bin/identityd
USER nonroot
ENTRYPOINT ["/usr/local/bin/identityd"]
