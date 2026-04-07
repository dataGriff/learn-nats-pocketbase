package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/nats-io/nats.go"
)

func main() {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = nats.DefaultURL
	}

	nc, err := nats.Connect(natsURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		log.Fatalf("Failed to connect to NATS at %s: %v", natsURL, err)
	}
	defer nc.Close()

	log.Printf("Connected to NATS at %s", natsURL)
	log.Println("Subscribing to order domain events...")

	// Subscribe to all order events using a wildcard subject.
	if _, err := nc.Subscribe("orders.*", func(msg *nats.Msg) {
		switch msg.Subject {
		case "orders.created":
			log.Printf("[ANALYTICS] New order placed – tracking funnel entry. Payload: %s", string(msg.Data))
		case "orders.updated":
			log.Printf("[ANALYTICS] Order updated – tracking status progression. Payload: %s", string(msg.Data))
		default:
			log.Printf("[ANALYTICS] Received event on %q: %s", msg.Subject, string(msg.Data))
		}
	}); err != nil {
		log.Fatalf("Failed to subscribe: %v", err)
	}

	log.Println("Analytics subscriber is running. Press Ctrl+C to stop.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("Shutting down analytics subscriber...")
}
