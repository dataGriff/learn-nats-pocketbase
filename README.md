# learn-nats-pocketbase

A simple demo app showing how to combine **PocketBase** and **NATS** to deliver three integration patterns from a single domain:

1. **Realtime UI** – PocketBase's built-in Server-Sent Events (SSE) pushes live order updates to every browser tab without polling.
2. **Decoupled domain events** – every time an order is created or its status changes, the PocketBase backend publishes a domain event (`orders.created` / `orders.updated`) to NATS so analytics, marketing, or any other bounded context can consume it independently.
3. **Outbox pattern** – events are persisted to a SQLite `_outbox` table before being forwarded to NATS, guaranteeing at-least-once delivery even if NATS is temporarily unavailable or the process restarts.

## Domain

☕ **Coffee Shop Order Tracking**

| Field           | Type     | Values                                      |
|-----------------|----------|---------------------------------------------|
| `customer_name` | text     | free text                                   |
| `items`         | JSON     | array of item names                         |
| `status`        | select   | `pending` → `preparing` → `ready` → `collected` |
| `total`         | number   | order total in £                            |

## Architecture

```
┌──────────────┐   REST / SSE    ┌─────────────────────────────────────┐
│   Browser    │◄───────────────►│         PocketBase backend           │
│  (frontend/) │                 │           (backend/)                 │
└──────────────┘                 │                                      │
                                 │  hook fires after record commit      │
                                 │         │                            │
                                 │         ▼                            │
                                 │  ┌─────────────┐   relay goroutine  │
                                 │  │  _outbox    │──────────────────► │──► NATS
                                 │  │  (SQLite)   │   polls every 2s   │
                                 │  └─────────────┘                    │
                                 └─────────────────────────────────────┘
                                                                        │
                                         ┌──────────────────────────────┘
                                         │ NATS publish
                                         ▼
                                ┌─────────────────────┐
                                │        NATS          │
                                │  orders.created      │
                                │  orders.updated      │
                                └──────────┬──────────┘
                                           │ subscribe
                                           ▼
                                ┌─────────────────────┐
                                │  Analytics consumer  │
                                │    (subscriber/)     │
                                └─────────────────────┘
```

## Project Structure

```
├── backend/          # Extended PocketBase (Go) with NATS event hooks
│   ├── main.go
│   ├── go.mod
│   └── Dockerfile
├── frontend/
│   └── index.html    # Single-page realtime order board (PocketBase JS SDK)
├── subscriber/       # Example analytics/marketing NATS consumer (Go)
│   ├── main.go
│   ├── go.mod
│   └── Dockerfile
└── docker-compose.yml
```

## Quick Start

### Prerequisites

- [Docker](https://docs.docker.com/get-docker/) with Compose v2

### Run everything

```bash
docker compose up --build
```

| Service    | URL                             | Description                        |
|------------|---------------------------------|------------------------------------|
| Backend    | <http://localhost:8090>         | PocketBase API + Admin UI          |
| Admin UI   | <http://localhost:8090/_/>      | Set up your admin account here     |
| Frontend   | open `frontend/index.html`      | Open directly in a browser         |
| NATS       | nats://localhost:4222           | Client connections                 |
| NATS mon.  | <http://localhost:8222>         | NATS HTTP monitoring               |

> **First run:** Visit <http://localhost:8090/_/> to create your PocketBase admin account.
> The `orders` collection is created automatically on first startup.

### Open the frontend

Because the frontend is a static HTML file, open it directly in a browser:

```bash
open frontend/index.html          # macOS
xdg-open frontend/index.html     # Linux
start frontend/index.html         # Windows
```

Or serve it with any static file server:

```bash
npx serve frontend
```

### Watch domain events flow to NATS

The `subscriber` container logs every domain event it receives:

```bash
docker compose logs -f subscriber
```

You'll see output like:

```
[ANALYTICS] New order placed – tracking funnel entry. Payload: {"record":{"customer_name":"Alice",...},"subject":"orders.created"}
[ANALYTICS] Order updated – tracking status progression. Payload: {"record":{"status":"ready",...},"subject":"orders.updated"}
```

## How It Works

### Realtime UI (PocketBase SSE)

The frontend uses the [PocketBase JS SDK](https://github.com/pocketbase/js-sdk) to subscribe to the `orders` collection:

```js
await pb.collection('orders').subscribe('*', function (e) {
  // e.action is 'create' | 'update' | 'delete'
  // e.record is the full order record
  refreshUI(e);
});
```

Any browser tab that has the page open will see status changes instantly.

### Domain Events via Outbox Pattern (NATS)

PocketBase has no built-in outbox mechanism, so this is implemented as a bespoke but lightweight addition.

#### Why an outbox?

Publishing directly to NATS from a hook risks silently dropping events if NATS is temporarily unavailable (e.g. restarts, network blips). The outbox pattern solves this by decoupling the *write* from the *publish*:

1. The record is saved to SQLite and an outbox row is written in the same hook handler.
2. A background relay goroutine polls the `_outbox` table every 2 seconds and publishes any unpublished rows to NATS.
3. Once NATS confirms the publish, the row is stamped with `published_at`.
4. If the backend restarts, the relay picks up any rows with `published_at IS NULL` and retries them.

This gives **at-least-once delivery** to NATS without needing an external message broker or a complex transaction coordinator.

#### Outbox table schema

```sql
CREATE TABLE IF NOT EXISTS _outbox (
    id           TEXT PRIMARY KEY,
    subject      TEXT NOT NULL,
    payload      TEXT NOT NULL,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    published_at DATETIME
);
```

The table is created automatically on first startup using PocketBase's raw DB access.

#### Hook → outbox write

`backend/main.go` registers two hooks that write to the outbox instead of publishing directly:

```go
app.OnRecordAfterCreateSuccess("orders").BindFunc(func(e *core.RecordEvent) error {
    writeOutboxRow(app, "orders.created", e.Record)
    return e.Next()
})

app.OnRecordAfterUpdateSuccess("orders").BindFunc(func(e *core.RecordEvent) error {
    writeOutboxRow(app, "orders.updated", e.Record)
    return e.Next()
})
```

These hooks fire **after** the database write succeeds, guaranteeing the outbox row reflects committed state.

#### Relay goroutine

```go
func relayPendingEvents(app *pocketbase.PocketBase, nc **nats.Conn) error {
    // 1. Read rows WHERE published_at IS NULL ORDER BY created_at
    // 2. Publish each to NATS
    // 3. Stamp published_at = NOW() on success
}
```

The relay starts inside `OnServe()` alongside the NATS connection so it is running before any API requests are accepted.

#### NATS consumer (subscriber)

The `subscriber` service (`subscriber/main.go`) demonstrates a decoupled consumer. It subscribes to the wildcard subject `orders.*` and receives events forwarded by the relay:

```go
nc.Subscribe("orders.*", func(msg *nats.Msg) {
    // analytics, marketing, inventory, etc.
})
```

Any number of independent consumers can subscribe without the backend knowing about them.

## Tech Stack

| Component  | Technology                                           |
|------------|------------------------------------------------------|
| Backend    | [PocketBase](https://pocketbase.io) v0.36 (Go)      |
| Messaging  | [NATS](https://nats.io) 2.10                         |
| Frontend   | Vanilla HTML/JS + PocketBase JS SDK v0.26            |
| Database   | SQLite (embedded in PocketBase)                      |
| Container  | Docker Compose                                       |