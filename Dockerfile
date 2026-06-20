FROM golang:1.22-alpine AS builder
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build \
    -ldflags="-X github.com/blocstor/bloc-csi/internal/driver.Version=${VERSION}" \
    -o /bloc-csi \
    ./cmd/driver

FROM alpine:3.20
RUN apk add --no-cache nbd-client e2fsprogs util-linux kmod
COPY --from=builder /bloc-csi /bloc-csi
ENTRYPOINT ["/bloc-csi"]
