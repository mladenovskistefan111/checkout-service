# Known limitations

- **No compensation:** if shipping fails after payment succeeded, the customer is charged and no order is created. Not idempotent either — retries re-charge.
- Health is static; readiness does not reflect downstream availability.
- Image is `amd64`-only and runs as root unless the pod overrides it.
- Single-attempt calls: no retries or timeouts beyond the incoming request context.
