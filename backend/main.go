package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

func main() {
	app := pocketbase.New()

	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = nats.DefaultURL
	}

	var nc *nats.Conn

	// Connect to NATS, ensure required tables/collections exist, and start the outbox relay.
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		var err error
		nc, err = nats.Connect(natsURL,
			nats.RetryOnFailedConnect(true),
			nats.MaxReconnects(-1),
		)
		if err != nil {
			log.Printf("Warning: could not connect to NATS at %s: %v", natsURL, err)
		} else {
			log.Printf("Connected to NATS at %s", natsURL)
		}

		if err := ensureOrdersCollection(app); err != nil {
			return err
		}

		if err := ensureOutboxTable(app); err != nil {
			return err
		}

		startOutboxRelay(app, &nc)

		return se.Next()
	})

	// Write to the outbox instead of publishing directly to NATS.
	// The relay goroutine handles delivery, providing at-least-once guarantees.
	// Note: these hooks fire after the record commit; returning an error here does
	// not roll back the record, but it does signal the API caller that something
	// went wrong so the failure is never silently swallowed.
	app.OnRecordAfterCreateSuccess("orders").BindFunc(func(e *core.RecordEvent) error {
		if err := writeOutboxRow(app, "orders.created", e.Record); err != nil {
			return fmt.Errorf("outbox write for orders.created: %w", err)
		}
		return e.Next()
	})

	app.OnRecordAfterUpdateSuccess("orders").BindFunc(func(e *core.RecordEvent) error {
		if err := writeOutboxRow(app, "orders.updated", e.Record); err != nil {
			return fmt.Errorf("outbox write for orders.updated: %w", err)
		}
		return e.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

// ensureOutboxTable creates the _outbox table if it does not already exist.
// This table acts as a durable, SQLite-backed event buffer between PocketBase and NATS.
func ensureOutboxTable(app *pocketbase.PocketBase) error {
	_, err := app.DB().NewQuery(`
		CREATE TABLE IF NOT EXISTS _outbox (
			id           TEXT PRIMARY KEY,
			subject      TEXT NOT NULL,
			payload      TEXT NOT NULL,
			created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
			published_at DATETIME
		)
	`).Execute()
	if err != nil {
		return fmt.Errorf("create _outbox table: %w", err)
	}
	log.Println("Outbox table ready.")
	return nil
}

// writeOutboxRow persists a domain event to the _outbox table.
// The outbox relay goroutine will pick it up and publish it to NATS asynchronously.
func writeOutboxRow(app *pocketbase.PocketBase, subject string, record *core.Record) error {
	payload, err := json.Marshal(map[string]any{
		"subject": subject,
		"record":  record.PublicExport(),
	})
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}

	_, err = app.DB().NewQuery(
		"INSERT INTO _outbox (id, subject, payload) VALUES ({:id}, {:subject}, {:payload})",
	).Bind(dbx.Params{
		"id":      uuid.NewString(),
		"subject": subject,
		"payload": string(payload),
	}).Execute()
	return err
}

// startOutboxRelay launches a background goroutine that polls _outbox every 2 seconds.
// Any rows with a NULL published_at are published to NATS and then marked as published.
// Events that were not published before a restart are automatically retried on the next run.
func startOutboxRelay(app *pocketbase.PocketBase, nc **nats.Conn) {
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := relayPendingEvents(app, nc); err != nil {
				log.Printf("Outbox relay error: %v", err)
			}
		}
	}()
	log.Println("Outbox relay started (polling every 2s).")
}

type outboxRow struct {
	ID      string `db:"id"`
	Subject string `db:"subject"`
	Payload string `db:"payload"`
}

// relayPendingEvents reads all unpublished _outbox rows and publishes them to NATS,
// marking each row as published after a successful Publish call.
//
// Delivery guarantee: at-least-once. If the process crashes after NATS confirms
// the publish but before the DB update completes, the row will be republished on
// the next relay cycle. Consumers should be idempotent; the record ID in the
// payload can be used as an idempotency key.
func relayPendingEvents(app *pocketbase.PocketBase, nc **nats.Conn) error {
	if *nc == nil || !(*nc).IsConnected() {
		return nil // NATS not yet available; will retry on the next tick
	}

	var rows []outboxRow
	if err := app.DB().NewQuery(
		"SELECT id, subject, payload FROM _outbox WHERE published_at IS NULL ORDER BY created_at",
	).All(&rows); err != nil {
		return fmt.Errorf("query _outbox: %w", err)
	}

	for _, row := range rows {
		if err := (*nc).Publish(row.Subject, []byte(row.Payload)); err != nil {
			log.Printf("Outbox relay: failed to publish row %s to %q: %v", row.ID, row.Subject, err)
			continue
		}
		if _, err := app.DB().NewQuery(
			"UPDATE _outbox SET published_at = datetime('now') WHERE id = {:id}",
		).Bind(dbx.Params{"id": row.ID}).Execute(); err != nil {
			log.Printf("Outbox relay: failed to mark row %s as published: %v", row.ID, err)
		} else {
			log.Printf("Outbox relay: published event to %q (row: %s)", row.Subject, row.ID)
		}
	}
	return nil
}

// ensureOrdersCollection creates the "orders" collection if it does not already exist.
func ensureOrdersCollection(app *pocketbase.PocketBase) error {
	if _, err := app.FindCollectionByNameOrId("orders"); err == nil {
		return nil
	}

	collection := core.NewBaseCollection("orders")

	collection.Fields.Add(&core.TextField{
		Name:     "customer_name",
		Required: true,
	})

	collection.Fields.Add(&core.JSONField{
		Name:     "items",
		Required: true,
	})

	collection.Fields.Add(&core.SelectField{
		Name:      "status",
		Required:  true,
		MaxSelect: 1,
		Values:    []string{"pending", "preparing", "ready", "collected"},
	})

	collection.Fields.Add(&core.NumberField{
		Name: "total",
	})

	// Open access rules for the demo (no authentication required).
	openRule := ""
	collection.ListRule = &openRule
	collection.ViewRule = &openRule
	collection.CreateRule = &openRule
	collection.UpdateRule = &openRule

	log.Println("Creating 'orders' collection...")
	return app.Save(collection)
}
