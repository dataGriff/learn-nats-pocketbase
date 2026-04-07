# learn-nats-pocketbase

A simple demo app showing how to combine **PocketBase** and **NATS** to deliver two integration patterns from a single domain:

1. **Realtime UI** – PocketBase's built-in Server-Sent Events (SSE) pushes live order updates to every browser tab without polling.
2. **Decoupled domain events** – every time an order is created or its status changes, the PocketBase backend publishes a domain event (`orders.created` / `orders.updated`) to NATS so analytics, marketing, or any other bounded context can consume it independently.

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
┌──────────────┐   REST / SSE    ┌─────────────────────┐
│   Browser    │◄───────────────►│  PocketBase backend  │
│  (frontend/) │                 │    (backend/)        │
└──────────────┘                 └──────────┬──────────┘
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

### Domain Events (NATS)

`backend/main.go` registers two PocketBase hooks:

```go
app.OnRecordAfterCreateSuccess("orders").BindFunc(func(e *core.RecordEvent) error {
    publishEvent(nc, "orders.created", e.Record)
    return e.Next()
})

app.OnRecordAfterUpdateSuccess("orders").BindFunc(func(e *core.RecordEvent) error {
    publishEvent(nc, "orders.updated", e.Record)
    return e.Next()
})
```

These hooks fire **after** the database write succeeds, guaranteeing that the event reflects committed state.

The subscriber (`subscriber/main.go`) demonstrates decoupled integration:

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