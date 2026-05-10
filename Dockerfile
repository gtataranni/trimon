# syntax=docker/dockerfile:1

# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/trimon \
      ./cmd/trimon

# ── Final stage ───────────────────────────────────────────────────────────────
FROM scratch

# CAP_NET_RAW is set at runtime, not baked into the image layer.
# Run with: docker run --cap-add NET_RAW ...
COPY --from=builder /out/trimon /trimon
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

ENTRYPOINT ["/trimon"]
CMD ["--config", "/etc/trimon/config.yaml"]
