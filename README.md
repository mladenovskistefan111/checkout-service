# checkout-service

A gRPC service that orchestrates the full order placement flow for the platform-demo e-commerce platform. It coordinates multiple downstream services — cart, product catalog, currency conversion, shipping, payment, and email — to process a customer order end-to-end. Part of a broader microservices platform built with full observability, GitOps, and internal developer platform tooling.

## Overview

The service exposes a single gRPC method:

| Method | Description |
|---|---|
| `PlaceOrder` | Orchestrates cart retrieval, pricing, shipping quote, payment charge, and order confirmation email |

**Port:** `5050` (gRPC)  
**Metrics Port:** `9090` (Prometheus + pprof)  
**Protocol:** gRPC  
**Language:** Go  

### What `PlaceOrder` does

1. Fetches the user's cart from `cart-service`
2. Resolves product prices via `product-catalog-service` and converts them to the user's currency via `currency-service`
3. Gets a shipping quote from `shipping-service` and converts it to the user's currency
4. Charges the credit card via `payment-service`
5. Ships the order via `shipping-service`
6. Empties the user's cart
7. Sends an order confirmation email via `email-service`

## Requirements

- Go 1.25+
- Docker
- Running instances of all downstream services (or stubs for local development)
- `grpcurl` for manual testing

## Environment Variables

| Variable | Required | Description |
|---|---|---|
| `PORT` | No | gRPC server port (default: `5050`) |
| `METRICS_PORT` | No | Prometheus metrics + pprof port (default: `9090`) |
| `ENABLE_TRACING` | No | Set to `1` to enable OpenTelemetry tracing |
| `COLLECTOR_SERVICE_ADDR` | No | OTel collector address e.g. `alloy:4317` (required if tracing enabled) |
| `OTEL_SERVICE_NAME` | No | Service name reported to OTel (default: `checkout-service`) |
| `ENABLE_PROFILING` | No | Set to `1` to enable continuous profiling via Pyroscope |
| `PYROSCOPE_ADDR` | No | Pyroscope server address (default: `http://pyroscope:4040`) |
| `PRODUCT_CATALOG_SERVICE_ADDR` | No | (default: `product-catalog-service:3550`) |
| `CART_SERVICE_ADDR` | No | (default: `cart-service:7070`) |
| `CURRENCY_SERVICE_ADDR` | No | (default: `currency-service:7000`) |
| `SHIPPING_SERVICE_ADDR` | No | (default: `shipping-service:50051`) |
| `PAYMENT_SERVICE_ADDR` | No | (default: `payment-service:50051`) |
| `EMAIL_SERVICE_ADDR` | No | (default: `email-service:8080`) |

## Running Locally

### 1. Build and run the service

```bash
go build ./...
go run ./cmd/server
```

### 2. Run with Docker

```bash
docker build -t checkout-service:local .

docker run -p 5050:5050 -p 9090:9090 \
  -e PRODUCT_CATALOG_SERVICE_ADDR="product-catalog-service:3550" \
  checkout-service:local
```

> **Note:** All six downstream services must be reachable at startup. For local development without the full stack, point missing services at a no-op gRPC stub.

## Testing

### Manual gRPC testing

Install `grpcurl` then:

```bash
# place an order
grpcurl -plaintext -d '{
  "user_id": "test-user",
  "user_currency": "USD",
  "address": {
    "street_address": "123 Main St",
    "city": "New York",
    "state": "NY",
    "country": "US",
    "zip_code": 10001
  },
  "email": "test@example.com",
  "credit_card": {
    "credit_card_number": "4111111111111111",
    "credit_card_cvv": 123,
    "credit_card_expiration_year": 2030,
    "credit_card_expiration_month": 12
  }
}' localhost:5050 hipstershop.CheckoutService/PlaceOrder

# health check
grpcurl -plaintext localhost:5050 grpc.health.v1.Health/Check

# prometheus metrics
curl localhost:9090/metrics

# pprof
curl localhost:9090/debug/pprof/
```

### Load testing

```bash
for i in $(seq 1 20); do
  grpcurl -plaintext -d '{
    "user_id": "load-test-user",
    "user_currency": "USD",
    "address": {"street_address": "123 Main St", "city": "New York", "state": "NY", "country": "US", "zip_code": 10001},
    "email": "test@example.com",
    "credit_card": {"credit_card_number": "4111111111111111", "credit_card_cvv": 123, "credit_card_expiration_year": 2030, "credit_card_expiration_month": 12}
  }' localhost:5050 hipstershop.CheckoutService/PlaceOrder
  sleep 0.2
done
```

## Project Structure

```
├── cmd/server/         # Binary entrypoint — server init, tracing, profiling, gRPC setup
├── internal/checkout/  # Business logic — PlaceOrder orchestration and downstream calls
├── internal/money/     # Money arithmetic helpers (sum, multiply)
├── proto/              # Proto definition and generated gRPC code
├── docs/               # Architecture decisions, runbooks, service contract
├── Dockerfile
├── go.mod
└── go.sum
```

## Observability

The service is fully instrumented across all four signals:

| Signal | Implementation |
|---|---|
| **Traces** | OpenTelemetry — exported to Alloy via OTLP gRPC, stored in Tempo |
| **Metrics** | `go-grpc-prometheus` — scraped by Alloy, stored in Mimir |
| **Logs** | `logrus` JSON — collected by Alloy from Docker, stored in Loki |
| **Profiles** | Pyroscope Go SDK — CPU, alloc objects, alloc space, inuse objects, inuse space |

## Documentation

See [`docs/`](./docs) for:

- Service contract and proto definition
- Architecture decision records
- Observability (metrics, traces, logs, profiles)
- Runbook

## Part Of

This service is part of [platform-demo](https://github.com/mladenovskistefan111) — a full platform engineering project featuring microservices, observability (LGTM stack), GitOps (Argo CD), policy enforcement (Kyverno), infrastructure provisioning (Crossplane), and an internal developer portal (Backstage).