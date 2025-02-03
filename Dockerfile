# =========================
# 1) Builder Stage
# =========================
FROM golang:1.23.5-alpine AS builder

RUN apk update && apk add --no-cache git bash

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION
ARG GIT_COMMIT
ARG BUILD_DATE

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -a -ldflags "-s -w" \
    -o /kube-ipmi

# =========================
# 2) Final Stage
# =========================
FROM alpine:3.18

# Install both ipmitool and dmidecode
RUN apk add --no-cache ipmitool dmidecode

# Copy the statically built binary
COPY --from=builder /kube-ipmi /kube-ipmi

ENTRYPOINT ["/kube-ipmi"]
