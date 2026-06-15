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

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /bloc-csi /bloc-csi
ENTRYPOINT ["/bloc-csi"]
