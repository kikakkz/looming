# SPDX-License-Identifier: Apache-2.0
#
# identityd's phase-1 image (topology-l1 §2). Shape mirrors
# gateway/Dockerfile: component directory as context, GOPROXY as a
# build-arg, distroless nonroot runtime.

FROM golang:1.25 AS build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/identityd ./cmd/identityd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/identityd /usr/local/bin/identityd
USER nonroot
ENTRYPOINT ["/usr/local/bin/identityd"]
