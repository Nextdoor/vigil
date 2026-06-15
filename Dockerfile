# Build the manager binary
FROM golang:1.26@sha256:87a41d2539e5671777734e91f467499ed5eafb1fb1f77221dff2744db7a51775 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Cache deps before copying source
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/

# Build
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -a -o manager cmd/main.go

# Runtime image
FROM gcr.io/distroless/static:nonroot
LABEL org.opencontainers.image.licenses="Apache-2.0"
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
