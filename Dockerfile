ARG GO_VERSION=1.24.3

FROM golang:${GO_VERSION} AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN	CGO_ENABLED=0 go build \
    -trimpath \
    -tags timetzdata \
    -o copilot-proxy \
    main.go


FROM gcr.io/distroless/static-debian12

USER 1001:1001
WORKDIR /app

COPY --from=builder --chown=1001:1001 /app/copilot-proxy .

ENTRYPOINT ["/app/copilot-proxy"]

