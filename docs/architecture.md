# Architecture

## Why this shape

The banking service separates HTTP, business rules, persistence and configuration. This
baseline keeps that separation in Go without copying Spring specifics:

| Banking                  | parking-service            | Notes                                              |
| ------------------------ | -------------------------- | -------------------------------------------------- |
| `BankingApplication`     | `cmd/api/main.go`          | wiring, HTTP server, graceful shutdown             |
| `config/*Properties`     | `internal/config`          | environment variables instead of properties files  |
| `entity`                 | `internal/domain`          | plain structs, no framework annotations            |
| `repository`             | `internal/store`           | interfaces; `store/memory` is the first impl       |
| `service`                | `internal/service`         | business rules, injected clock                     |
| `controller` + `dto`     | `internal/httpapi`         | chi routes, request/response shapes                |
| `GlobalExceptionHandler` | `internal/httpapi/errors.go` | problem+json mapping for every failure           |

Dependencies point inward: `httpapi` → `service` → `store` + `domain`. Nothing in `domain`
or `service` imports chi, `net/http` or the memory package.

## Request flow

1. `middleware.RequestID`, `RealIP` and structured request logging run for every call.
2. `middleware.Recoverer` turns a panic into a 500; `middleware.Timeout` caps handler time
   at 10 seconds.
3. A handler decodes JSON with unknown fields rejected and a 1 MiB body limit.
4. The service validates input and applies the rule (capacity, session state).
5. `fail` maps domain errors to a problem+json response with a stable `code`; unexpected
   errors are logged with the request id and returned as a generic 500.

## Boundaries to keep

- **Store interfaces** are the only persistence seam. Adding Postgres means a new package
  implementing `store.LotStore` and `store.SessionStore`, plus migrations; services and
  handlers do not change.
- **Domain errors** (`ErrNotFound`, `ErrLotFull`, `ErrSessionNotActive`, `ValidationError`)
  are the contract between services and HTTP. Handlers do not inspect strings.
- **Clock injection** (`func() time.Time`) keeps time-dependent tests deterministic.
- **External providers** (SMS, payments, gate control) should follow the same pattern: an
  interface in the consuming package, a real implementation, and a no-op or log-only
  implementation selected by configuration for local development and tests.
- **Stripe lives in one package.** `internal/payments/stripe` is the only package that holds
  the Stripe client, the secret key and the webhook signature check; it implements
  `payment.Provider`, and `internal/payments` picks between it and the fake provider from
  configuration. `internal/service` and `internal/domain` depend on `payment.Provider` alone,
  so a provider change never reaches the business rules.
