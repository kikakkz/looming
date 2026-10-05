# SPDX-License-Identifier: Apache-2.0
#
# looming-ctl's phase-1 image (topology-l1 §2). Shape mirrors
# gateway/Dockerfile: component directory as context, GOPROXY as a
# build-arg, distroless nonroot runtime. The ctl's cmd placement under
# topology/ is a T1 temporary pending #108's CLI layout decision.

FROM golang:1.25 AS build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/looming-ctl ./cmd/looming-ctl

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/looming-ctl /usr/local/bin/looming-ctl
USER nonroot
ENTRYPOINT ["/usr/local/bin/looming-ctl"]
