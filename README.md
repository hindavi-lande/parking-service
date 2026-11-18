# parking-service

Minimal Go + [chi](https://github.com/go-chi/chi) baseline for a parking domain. It is the
starting point for the `PARK-*` stories and follows the same layered shape as the `banking`
service: thin HTTP handlers, business rules in services, persistence behind repository
interfaces, and configuration from the environment.

The baseline covers three things end to end:

- **Lots** — register a lot with a name, address, capacity and optional hourly rate, then
  read it back.
- **Sessions** — check a vehicle in (plate + space kind), enforce capacity, and check it out.
- **Payments** — price a finished session in whole started hours and track the payment from
  `PENDING` to `PAID` or `FAILED` behind a provider interface.

## Requirements

- Go 1.24+
- No database, broker or third-party account is needed to run or test the service.

## Run

```bash
make run
# or
go run ./cmd/api
```

The API listens on `:8080` by default. `PAYMENT_HOURLY_RATE` is the one setting with no
default, so set it before starting — for example `PAYMENT_HOURLY_RATE=5000 make run`.
Check it:

```bash
curl -s localhost:8080/healthz
# {"status":"ok"}
```

## API

Everything is versioned under `/api/v1`. Times are UTC, RFC 3339.

| Method | Path                              | Purpose                                            |
| ------ | --------------------------------- | -------------------------------------------------- |
| `GET`  | `/healthz`                        | liveness probe                                     |
| `POST` | `/api/v1/lots`                    | create a lot (`name`, `address`, `capacity`, optional `hourlyRate`) |
| `GET`  | `/api/v1/lots`                    | list lots, oldest first                            |
| `GET`  | `/api/v1/lots/{lotID}`            | read one lot                                       |
| `POST` | `/api/v1/lots/{lotID}/check-ins`  | check a vehicle in (`plate`, optional `kind`, optional `phone`) |
| `GET`  | `/api/v1/sessions/{sessionID}`    | read one session                                   |
| `POST` | `/api/v1/sessions/{sessionID}/checkout` | end an active session and create its payment; a repeated call returns that same payment |
| `GET`  | `/api/v1/payments/{paymentID}`    | read one payment with its status and timestamps    |
| `POST` | `/api/v1/payments/webhooks/{provider}` | apply a provider callback to a pending payment |
| `POST` | `/api/v1/payments/stripe/webhook` | apply a Stripe callback after verifying its signature |
| `POST` | `/api/v1/payments/razorpay/webhook` | apply a Razorpay callback after verifying its signature |

`kind` is `STANDARD` (default) or `EV`. Plates are trimmed, internal whitespace is
collapsed and the value is upper-cased. A plate is 2–15 characters: letters, digits, spaces
and hyphens.

`hourlyRate` is optional on a lot and is an integer in minor units (5000 is INR 50.00). When
it is omitted the lot stores `null` and the payment falls back to `PAYMENT_HOURLY_RATE`; `0`
is accepted and means free parking. A negative rate is rejected with `400 VALIDATION_FAILED`
on field `hourlyRate`, and the lot is not stored.

`phone` is optional and must already be E.164: a `+` followed by 8–15 digits, first digit
non-zero. Anything else is rejected with `400 VALIDATION_FAILED` before the session is
created. The number is used only to send the confirmation message described in
[Notifications](#notifications); it is not stored on the session and not returned in the
session response.

Example:

```bash
# create a lot with two spaces and an explicit hourly rate of INR 50.00
curl -s -X POST localhost:8080/api/v1/lots \
  -H 'Content-Type: application/json' \
  -d '{"name":"Central Plaza","address":"MG Road","capacity":2,"hourlyRate":5000}'

# check in
curl -s -X POST localhost:8080/api/v1/lots/<lotID>/check-ins \
  -H 'Content-Type: application/json' \
  -d '{"plate":"ka 01 ab 1234","kind":"EV","phone":"+14155550123"}'

# check out — the response carries the new payment alongside the session
curl -s -X POST localhost:8080/api/v1/sessions/<sessionID>/checkout
```

### Payments

Every checkout prices the finished session and creates exactly one `PENDING` payment for it:

- The fee is the stay rounded up to the next started hour with a one-hour minimum, multiplied
  by the hourly rate. A one-minute stay, a stay of exactly 60 minutes and a zero-length stay
  are each billed one hour; 60 minutes 1 second and 90 minutes are billed two hours.
- Amounts are integer minor units (5000 is INR 50.00), never floats. The currency is fixed at
  `INR`.
- The rate is the lot's `hourlyRate`. A lot without one uses `PAYMENT_HOURLY_RATE`; `0` on
  the lot means free parking.
- The session is written as checked out before the payment is opened, so a pricing or provider
  failure leaves the vehicle checked out with a `PENDING` payment instead of back in the lot.
- The payment is stored before the provider is asked, so a retried checkout finds that payment
  instead of opening a second one, and a session can never hold two payments. Checking out an
  already checked-out session is idempotent: it answers `200` with the payment the first
  checkout made, so a client that lost the response can simply retry.
- `GET /api/v1/payments/{paymentID}` returns the payment with its amount, currency, provider,
  status, `createdAt` and `updatedAt`, plus the optional `clientSecret` and `failureMessage`
  described under [Stripe](#stripe) and the optional `orderId` and `keyId` described under
  [Razorpay](#razorpay).
- `POST /api/v1/payments/webhooks/{provider}` applies a provider callback. The path
  must name the provider `PAYMENT_PROVIDER` selected; any other name fails with a
  `400 VALIDATION_FAILED` problem. A callback moves a `PENDING` payment to `PAID` or
  `FAILED`; once a payment reached either status it cannot change again and the
  callback fails with a `409 PAYMENT_NOT_PENDING` problem.

The default `fake` provider needs no account. Its callback body is
`{"paymentId":"<paymentID>","status":"PAID"}` (or `"FAILED"`):

```bash
curl -s -X POST localhost:8080/api/v1/payments/webhooks/fake \
  -H 'Content-Type: application/json' \
  -d '{"paymentId":"<paymentID>","status":"PAID"}'
```

### Stripe

With `PAYMENT_PROVIDER=stripe` the checkout opens a Stripe PaymentIntent for the same amount
and currency and returns the payment with an optional `clientSecret`: the single-use secret
the client confirms the PaymentIntent with. Card data never reaches this service — the client
confirms the PaymentIntent directly with Stripe. The payment also carries an optional
`failureMessage`, filled in when a callback reports why a card was declined. Both fields are
omitted from the JSON while they are empty, so a payment from the fake provider keeps exactly
its old shape.

Register `https://<your-host>/api/v1/payments/stripe/webhook` as the webhook URL in the
Stripe dashboard. The route verifies the `Stripe-Signature` header against the raw request
body with `STRIPE_WEBHOOK_SECRET`; a callback that does not verify answers
`400 VALIDATION_FAILED` and changes nothing. `payment_intent.succeeded` marks the payment
`PAID` and `payment_intent.payment_failed` marks it `FAILED` together with the failure
message. Handling is idempotent: a repeated event id, or an event for a payment that already
reached a terminal status, answers `200` and changes nothing. The generic route
`/api/v1/payments/webhooks/{provider}` keeps its old behaviour for every other provider and
still answers `409 PAYMENT_NOT_PENDING` for a terminal payment.

A Stripe timeout or 5xx answers `502 PROVIDER_ERROR`. The payment stays `PENDING` and the
session stays checked out, so the same checkout can be retried without opening a second
payment.

### Razorpay

With `PAYMENT_PROVIDER=razorpay` the checkout opens a Razorpay order for the same amount,
with `receipt` set to the payment id and `notes.sessionId` set to the session. Razorpay
amounts are in paise, which are already minor units, so the amount passes through with no
float conversion. The payment response carries two optional fields next to `amount`: the
`orderId` of the order and the publishable `keyId` (`RAZORPAY_KEY_ID`) Razorpay Checkout
needs, so the client can offer UPI, card or netbanking. The key secret never leaves the
service and card data never reaches it. Both fields are omitted from the JSON while they are
empty, so a payment from the fake or Stripe provider keeps exactly its old shape.

Register `https://<your-host>/api/v1/payments/razorpay/webhook` as the webhook URL in the
Razorpay dashboard. The route verifies the `X-Razorpay-Signature` header against the raw
request body with `RAZORPAY_WEBHOOK_SECRET`; a callback that does not verify answers
`400 VALIDATION_FAILED` and changes nothing. `payment.captured` marks the payment `PAID` and
`payment.failed` marks it `FAILED` together with the failure reason. A captured event whose
amount or currency does not match the stored payment answers `400 VALIDATION_FAILED`, is
logged, and leaves the payment `PENDING`; its event id is recorded as consumed, so re-posting
it answers `200` without changes. Handling is idempotent: a repeated event id, or an
event for a payment that already reached a terminal status, answers `200` and changes
nothing.

A Razorpay timeout or 5xx answers `502 PROVIDER_ERROR`. The payment stays `PENDING` and the
session stays checked out, so the same checkout can be retried without opening a second
order.

### Errors

Failures use [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) `application/problem+json`,
the same shape as the banking service, plus a stable `code`:

```json
{
  "type": "about:blank",
  "title": "Conflict",
  "status": 409,
  "detail": "The lot has no free capacity.",
  "instance": "/api/v1/lots/abc/check-ins",
  "code": "LOT_FULL"
}
```

| Status | Code                  | When                                              |
| ------ | --------------------- | ------------------------------------------------- |
| `400`  | `VALIDATION_FAILED`   | missing or malformed input, unknown JSON field or webhook provider |
| `404`  | `NOT_FOUND`           | unknown lot, session or payment                   |
| `409`  | `LOT_FULL`            | no active capacity left in the lot                 |
| `409`  | `PAYMENT_NOT_PENDING` | the payment already reached a final status         |
| `500`  | `INTERNAL_ERROR`      | unexpected failure; details are logged, not returned |
| `502`  | `PROVIDER_ERROR`      | the payment provider timed out or answered 5xx; the payment is still `PENDING` |

### Notifications

A check-in that carries a valid `phone` sends one confirmation SMS once the session exists:

```text
Checked in to Central Plaza. Plate KA 01 AB 1234. Start 2024-05-01T10:15:00Z.
```

- The message names the lot, the plate and the session start time in UTC, RFC 3339.
- The whole body fits one 160-character SMS segment. When a long lot name would push it over,
  only the lot name is shortened and marked with `...`; the plate and the start time are
  always kept intact.
- One best-effort attempt is made after the session is created, before the check-in response
  is returned, bounded by a short timeout. A provider rejection or timeout is logged with the
  session id; the check-in still succeeds and the session stays active.
- A check-in without `phone` sends nothing.
- Checkout messages, message templates and delivery reports are not implemented.
- With no Twilio settings — or with any one of them missing — a log-only notifier writes the
  recipient and the body to the log and nothing is sent, so local development and tests need
  no Twilio account.

## Configuration

Every setting has a working default except `PAYMENT_HOURLY_RATE`, which is required: the
service logs the configuration error and exits without listening when it is missing,
non-numeric or negative. Copy `.env.example` to `.env` only when you need to override
something, then export it.

| Variable                 | Default | Purpose                             |
| ------------------------ | ------- | ----------------------------------- |
| `APP_ENV`                | `local` | environment name, included in logs  |
| `APP_ADDR`               | `:8080` | listen address                      |
| `HTTP_READ_TIMEOUT`      | `5s`    | read timeout                        |
| `HTTP_WRITE_TIMEOUT`     | `10s`   | write timeout                       |
| `HTTP_IDLE_TIMEOUT`      | `60s`   | keep-alive idle timeout             |
| `HTTP_SHUTDOWN_TIMEOUT`  | `10s`   | grace period for in-flight requests |
| `PAYMENT_PROVIDER`       | `fake`  | payment provider: `fake`, `stripe` or `razorpay` |
| `PAYMENT_HOURLY_RATE`    | –       | required default hourly rate in minor units |
| `STRIPE_SECRET_KEY`      | –       | required when `PAYMENT_PROVIDER=stripe`; never committed |
| `STRIPE_WEBHOOK_SECRET`  | –       | required when `PAYMENT_PROVIDER=stripe`; never committed |
| `STRIPE_API_BASE_URL`    | –       | optional Stripe API base URL; Stripe itself when empty |
| `RAZORPAY_KEY_ID`        | –       | publishable key id; required when `PAYMENT_PROVIDER=razorpay` |
| `RAZORPAY_KEY_SECRET`    | –       | required when `PAYMENT_PROVIDER=razorpay`; never committed |
| `RAZORPAY_WEBHOOK_SECRET` | –      | required when `PAYMENT_PROVIDER=razorpay`; never committed |
| `RAZORPAY_API_BASE_URL`  | –       | optional Razorpay API base URL; Razorpay itself when empty |
| `TWILIO_ACCOUNT_SID`     | –       | Twilio account SID for the confirmation SMS |
| `TWILIO_AUTH_TOKEN`      | –       | Twilio auth token; never logged or printed  |
| `TWILIO_FROM_NUMBER`     | –       | sender number the confirmation SMS comes from |

`PAYMENT_HOURLY_RATE` has no default and is read as an integer in minor units (`5000` is
INR 50.00), so the process cannot start without a usable rate. It prices every lot that was
created without an `hourlyRate` of its own. `PAYMENT_PROVIDER` selects the provider: `fake`
performs no network call, `stripe` calls the Stripe API, `razorpay` calls the Razorpay API,
and any other name fails startup. Selecting `stripe` without `STRIPE_SECRET_KEY` or
`STRIPE_WEBHOOK_SECRET` also fails startup; the error names the missing variable and never
echoes a value, and neither secret belongs in the repository. `STRIPE_API_BASE_URL` is
optional and points at Stripe itself when it is empty. Selecting `razorpay` fails the same
way when `RAZORPAY_KEY_ID`, `RAZORPAY_KEY_SECRET` or `RAZORPAY_WEBHOOK_SECRET` is missing,
and `RAZORPAY_API_BASE_URL` is the optional base URL override for that provider. The
currency is not configurable: payments are always in `INR`.

The three `TWILIO_*` settings have no defaults and hold no credential in the repository. The
SMS provider is used only when all three are set; if any one of them is missing, the service
starts normally on the log-only notifier, which is what local development and tests use.

## Project layout

```text
cmd/api/                 entrypoint, wiring, graceful shutdown
internal/config/         environment configuration
internal/domain/         entities and domain errors
internal/store/          repository interfaces
internal/store/memory/   in-memory implementation (local + tests)
internal/service/        business rules
internal/payment/        provider interface and the no-account fake implementation
internal/payments/       provider selection, plus the Stripe client in internal/payments/stripe
                         and the Razorpay client in internal/payments/razorpay
internal/httpapi/        chi router, handlers, DTOs, problem+json errors, middleware
docs/                    architecture notes
```

The mapping to the banking service is deliberate: `internal/domain` ≈ `entity`,
`internal/store` ≈ `repository`, `internal/service` ≈ `service`, `internal/httpapi` ≈
`controller` + `dto` + `exception`, and `cmd/api` ≈ the Spring Boot application class.
See [docs/architecture.md](docs/architecture.md) for the decisions behind the baseline.

## Tests

```bash
make check   # gofmt, go vet, go test ./...
```

Handlers are exercised through the real chi router with `httptest`, including the
capacity rule, the fee rule, payment status transitions, validation failures and error
bodies. Services have unit tests with an injected clock.

## Design notes

- **Persistence is in memory.** `store.LotStore`, `store.SessionStore` and
  `store.PaymentStore` are the seams for Postgres; nothing above them imports the memory
  package. The data is intentionally lost on restart until a persistence story lands.
- **Capacity is enforced in one place.** `SessionService.CheckIn` holds a mutex across the
  active-session count and the insert; a database transaction will replace it when the
  store becomes transactional.
- **No third-party clients in business code.** Notifications and payments sit behind their
  own interfaces (`notify.Notifier`, `payment.Provider`) and are selected from the
  environment; `internal/service` and `internal/domain` import no provider SDK or HTTP
  client.
- **One fee rule.** `PaymentService` owns the money rules: whole started hours with a
  one-hour minimum, integer minor units, currency fixed at `INR`.
- **One API style.** Versioned paths, problem+json errors, request ids, structured logs and
  graceful shutdown are in place before the first feature is added.
