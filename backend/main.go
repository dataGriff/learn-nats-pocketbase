package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/nats-io/nats.go"
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

	// Connect to NATS and ensure the orders collection exists when the server starts.
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

		return se.Next()
	})

	// Publish a domain event every time an order is successfully created.
	app.OnRecordAfterCreateSuccess("orders").BindFunc(func(e *core.RecordEvent) error {
		publishEvent(nc, "orders.created", e.Record)
		return e.Next()
	})

	// Publish a domain event every time an order is successfully updated.
	app.OnRecordAfterUpdateSuccess("orders").BindFunc(func(e *core.RecordEvent) error {
		publishEvent(nc, "orders.updated", e.Record)
		return e.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

// publishEvent marshals the record and publishes it to the given NATS subject.
func publishEvent(nc *nats.Conn, subject string, record *core.Record) {
	if nc == nil || !nc.IsConnected() {
		log.Printf("NATS not connected, skipping publish to %q", subject)
		return
	}

	payload, err := json.Marshal(map[string]any{
		"subject": subject,
		"record":  record.PublicExport(),
	})
	if err != nil {
		log.Printf("Failed to marshal record for %q: %v", subject, err)
		return
	}

	if err := nc.Publish(subject, payload); err != nil {
		log.Printf("Failed to publish to %q: %v", subject, err)
		return
	}

	log.Printf("Published event to %q (record: %s)", subject, record.Id)
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
