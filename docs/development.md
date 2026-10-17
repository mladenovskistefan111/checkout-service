# Develop & test

## Running locally

Requirements: Go 1.25+, Docker, [`grpcurl`](https://github.com/fullstorydev/grpcurl), and the six downstream services (or stand-ins) for a *successful* order.

```bash
go build ./... && go run ./cmd/server

docker build -t checkout-service:local .
docker run --rm -p 5050:5050 -p 9090:9090 \
  -e CART_SERVICE_ADDR=host.docker.internal:7070 \
  -e PRODUCT_CATALOG_SERVICE_ADDR=host.docker.internal:3550 \
  checkout-service:local

go test ./...
```

The simplest full setup is the compose file in `platform-demo-observability/`.

### Manual gRPC testing (reflection is on)

```bash
grpcurl -plaintext -d '{
  "user_id": "test-user",
  "user_currency": "USD",
  "address": {"street_address": "123 Main St", "city": "New York", "state": "NY", "country": "US", "zip_code": 10001},
  "email": "test@example.com",
  "credit_card": {"credit_card_number": "4111111111111111", "credit_card_cvv": 123,
                  "credit_card_expiration_year": 2030, "credit_card_expiration_month": 12}
}' localhost:5050 hipstershop.CheckoutService/PlaceOrder

grpcurl -plaintext localhost:5050 grpc.health.v1.Health/Check
curl -s localhost:9090/metrics | grep grpc_server
```

The user's cart must already contain items (add some via `cart-service`), otherwise the order has zero lines and a total of 0.

## Project structure

```
├── cmd/server/main.go         # entrypoint: tracing, profiling, gRPC server, downstream dials, metrics server
├── internal/checkout/         # PlaceOrder orchestration (service.go)
├── internal/money/            # Money arithmetic (Sum, MultiplySlow)
├── proto/                     # checkout.proto + generated Go (checkout.pb.go, checkout_grpc.pb.go)
├── Dockerfile
├── go.mod / go.sum
└── docs/                      # empty placeholder
```
