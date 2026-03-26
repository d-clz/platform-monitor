# Stage 1: build both binaries
FROM golang:1.22-alpine AS builder

WORKDIR /build

# Download dependencies first (layer cached unless go.mod/go.sum change)
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /monitor   ./cmd/monitor
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /dashboard ./cmd/dashboard

# Stage 2: minimal runtime image
# alpine provides CA certs needed for HTTPS calls to GitLab and the kube API
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

# OCP assigns arbitrary UIDs at runtime; group-executable bits ensure
# the binary runs regardless of the assigned UID
COPY --from=builder --chmod=755 /monitor   /monitor
COPY --from=builder --chmod=755 /dashboard /dashboard
COPY --chmod=755 web/ /web/

EXPOSE 8080

# Default entrypoint is the monitor (CronJob).
# The dashboard Deployment overrides this with ["/dashboard"].
CMD ["/monitor"]
