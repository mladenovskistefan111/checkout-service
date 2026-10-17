# API

Package `hipstershop`, service `CheckoutService` ([`proto/checkout.proto`](https://github.com/platform-boutique/app-checkout-service/blob/main/proto/checkout.proto)). The proto file also contains client-side copies of the downstream service definitions.

| Method | Request | Response |
|---|---|---|
| `PlaceOrder` | `PlaceOrderRequest{ user_id, user_currency, Address address, email, CreditCardInfo credit_card }` | `PlaceOrderResponse{ OrderResult order }` (`order_id`, `shipping_tracking_id`, `shipping_cost`, `shipping_address`, `items[]`) |

### What `PlaceOrder` does (in order)

1. Generate an order UUID.
2. `cart-service.GetCart(user_id)`.
3. For every cart line, **in parallel**: `product-catalog-service.GetProduct` then `currency-service.Convert` (USD → `user_currency`).
4. `shipping-service.GetQuote` (USD) then `currency-service.Convert` to `user_currency`.
5. Sum items × quantity + shipping.
6. `payment-service.Charge(total, credit_card)` — failure → gRPC `INTERNAL`, order aborted.
7. `shipping-service.ShipOrder` — failure → gRPC `UNAVAILABLE`. **The card has already been charged at this point; there is no refund/compensation step.**
8. `cart-service.EmptyCart` — error ignored.
9. `email-service.SendOrderConfirmation` — error logged, **never fails the order**.

Failures in steps 2–5 return `INTERNAL` with the underlying message.
