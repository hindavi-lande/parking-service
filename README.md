# parking-service

Minimal Go + [chi](https://github.com/go-chi/chi) baseline for a parking domain. It is the
starting point for the `PARK-*` stories and follows the same layered shape as the `banking`
service: thin HTTP handlers, business rules in services, persistence behind repository
interfaces, and configuration from the environment.

The baseline covers two things end to end:

- **Lots** — register a lot with a name, address and capacity, then read it back.
- **Sessions** — check a vehicle in (plate + space kind), enforce capacity, and check it out.

## Requirements

- Go 1.24+
- No database, broker or third-party account is needed to run or test the service.

## Run

```bash
make run
# or
go run ./cmd/api
```

The API listens on `:8080` by default. Check it:

```bash
curl -s localhost:8080/healthz
# {"status":"ok"}
```

## API

Everything is versioned under `/api/v1`. Times are UTC, RFC 3339.

| Method | Path                              | Purpose                                            |
| ------ | --------------------------------- | -------------------------------------------------- |
| `GET`  | `/healthz`                        | liveness probe                                     |
| `POST` | `/api/v1/lots`                    | create a lot (`name`, `address`, `capacity`)        |
| `GET`  | `/api/v1/lots`                    | list lots, oldest first                            |
| `GET`  | `/api/v1/lots/{lotID}`            | read one lot                                       |
| `POST` | `/api/v1/lots/{lotID}/check-ins`  | check a vehicle in (`plate`, optional `kind`, optional `phone`) |
| `GET`  | `/api/v1/sessions/{sessionID}`    | read one session                                   |
| `POST` | `/api/v1/sessions/{sessionID}/checkout` | end an active session                        |

`kind` is `STANDARD` (default) or `EV`. Plates are trimmed, internal whitespace is
collapsed and the value is upper-cased. A plate is 2–15 characters: letters, digits, spaces
and hyphens.

`phone` is optional and, when present, must be an E.164 number such as `+14155552671`. The
session response repeats it as `phone` and omits the key when no number was given.

Example:

```bash
# create a lot with two spaces
curl -s -X POST localhost:8080/api/v1/lots \
  -H 'Content-Type: application/json' \
  -d '{"name":"Central Plaza","address":"MG Road","capacity":2}'

# check in and ask for the confirmation SMS
curl -s -X POST localhost:8080/api/v1/lots/<lotID>/check-ins \
  -H 'Content-Type: application/json' \
  -d '{"plate":"ka 01 ab 1234","kind":"EV","phone":"+14155552671"}'

# the response repeats the phone; without one the key is omitted
# {"id":"...","lotId":"...","plate":"KA 01 AB 1234","phone":"+14155552671", ...}

# check out
curl -s -X POST localhost:8080/api/v1/sessions/<sessionID>/checkout
```

### SMS confirmation

A check-in that carries a `phone` sends one confirmation SMS after the session is created. The
message names the lot, the plate and the session start time, rendered as RFC 3339 UTC:

```text
Central Plaza: KA 01 AB 1234 checked in at 2026-02-14T09:30:00Z
```

The body always fits one 160-character SMS segment; a lot name that would push it over is
shortened with `...`. Delivery is a single best-effort attempt: there is no retry and no queue,
so a Twilio rejection or timeout is logged with the session id and neither fails the check-in
nor removes the session. A check-in without a phone sends nothing, and a number that is not in
E.164 form is rejected with `400 VALIDATION_FAILED` before the session is created.

When the Twilio settings are absent a log-only notifier writes the same message at info level
along with the recipient and the session id, so local development and tests need no account.

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

| Status | Code                 | When                                              |
| ------ | -------------------- | ------------------------------------------------- |
| `400`  | `VALIDATION_FAILED`  | missing or malformed input, unknown JSON field     |
| `404`  | `NOT_FOUND`          | unknown lot or session                            |
| `409`  | `LOT_FULL`           | no active capacity left in the lot                 |
| `409`  | `SESSION_NOT_ACTIVE` | the session was already checked out                |
| `500`  | `INTERNAL_ERROR`     | unexpected failure; details are logged, not returned |

## Configuration

Every setting has a working default, so the service starts with an empty environment.
Copy `.env.example` to `.env` only when you need to override something, then export it.

| Variable                 | Default | Purpose                             |
| ------------------------ | ------- | ----------------------------------- |
| `APP_ENV`                | `local` | environment name, included in logs  |
| `APP_ADDR`               | `:8080` | listen address                      |
| `HTTP_READ_TIMEOUT`      | `5s`    | read timeout                        |
| `HTTP_WRITE_TIMEOUT`     | `10s`   | write timeout                       |
| `HTTP_IDLE_TIMEOUT`      | `60s`   | keep-alive idle timeout             |
| `HTTP_SHUTDOWN_TIMEOUT`  | `10s`   | grace period for in-flight requests |
| `TWILIO_ACCOUNT_SID`     | unset   | Twilio account SID; any missing value selects the log-only notifier |
| `TWILIO_AUTH_TOKEN`      | unset   | Twilio auth token; any missing value selects the log-only notifier |
| `TWILIO_FROM_NUMBER`     | unset   | number the SMS is sent from; any missing value selects the log-only notifier |

The three `TWILIO_*` settings are needed together: when any one of them is unset the service
starts normally and writes the confirmation message to the log instead of sending it. Real
values belong in your own environment — never commit a credential to the repository.

## Project layout

```text
cmd/api/                 entrypoint, wiring, graceful shutdown
internal/config/         environment configuration
internal/domain/         entities and domain errors
internal/store/          repository interfaces
internal/store/memory/   in-memory implementation (local + tests)
internal/service/        business rules
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
capacity rule, validation failures and error bodies. Services have unit tests with an
injected clock.

## Design notes

- **Persistence is in memory.** `store.LotStore` and `store.SessionStore` are the seams
  for Postgres; nothing above them imports the memory package. The data is intentionally
  lost on restart until a persistence story lands.
- **Capacity is enforced in one place.** `SessionService.CheckIn` holds a mutex across the
  active-session count and the insert; a database transaction will replace it when the
  store becomes transactional.
- **No third-party clients in business code.** Notifications, payments and gate hardware
  belong behind their own interfaces, configured from the environment.
- **One API style.** Versioned paths, problem+json errors, request ids, structured logs and
  graceful shutdown are in place before the first feature is added.
