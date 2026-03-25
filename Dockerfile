# --- Stage 1: Build ---
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -o /checkout-service \
    ./cmd/server

# --- Stage 2: Run ---
FROM alpine:3.21

WORKDIR /app

COPY --from=builder /checkout-service .

ENV GOTRACEBACK=single

EXPOSE 5050 9090

ENTRYPOINT ["/app/checkout-service"]