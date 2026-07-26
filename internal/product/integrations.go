package product

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
)

var ErrIgnoredEvent = errors.New("webhook event does not create an order")

type IntegrationOrder struct {
	IdempotencyKey string
	ExternalID     string
	Input          domain.CreateOrderInput
}

func ParseStripeOrder(payload []byte) (IntegrationOrder, error) {
	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object struct {
				ID                string            `json:"id"`
				ClientReferenceID string            `json:"client_reference_id"`
				Metadata          map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := decodeJSON(payload, &event, false); err != nil {
		return IntegrationOrder{}, fmt.Errorf("%w: decode Stripe event: %v", ErrInvalidInput, err)
	}
	if event.Type != "checkout.session.completed" {
		return IntegrationOrder{}, ErrIgnoredEvent
	}
	eventID := event.Data.Object.ClientReferenceID
	if eventID == "" {
		eventID = event.Data.Object.Metadata["event_id"]
	}
	quantity, err := strconv.Atoi(event.Data.Object.Metadata["quantity"])
	if event.ID == "" || eventID == "" || err != nil {
		return IntegrationOrder{}, fmt.Errorf("%w: Stripe id, event_id/client_reference_id and metadata.quantity are required", ErrInvalidInput)
	}
	return IntegrationOrder{
		IdempotencyKey: "stripe:" + event.ID,
		ExternalID:     event.Data.Object.ID,
		Input:          domain.CreateOrderInput{EventID: eventID, Quantity: quantity},
	}, nil
}

func ParseShopifyOrder(payload []byte) (IntegrationOrder, error) {
	var order struct {
		ID        json.Number `json:"id"`
		LineItems []struct {
			Quantity int `json:"quantity"`
		} `json:"line_items"`
		NoteAttributes []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"note_attributes"`
	}
	if err := decodeJSON(payload, &order, false); err != nil {
		return IntegrationOrder{}, fmt.Errorf("%w: decode Shopify order: %v", ErrInvalidInput, err)
	}
	eventID := ""
	for _, attribute := range order.NoteAttributes {
		if attribute.Name == "event_id" {
			eventID = attribute.Value
			break
		}
	}
	quantity := 0
	for _, item := range order.LineItems {
		quantity += item.Quantity
	}
	if order.ID == "" || eventID == "" {
		return IntegrationOrder{}, fmt.Errorf("%w: Shopify id and event_id note attribute are required", ErrInvalidInput)
	}
	externalID := order.ID.String()
	return IntegrationOrder{
		IdempotencyKey: "shopify:" + externalID,
		ExternalID:     externalID,
		Input:          domain.CreateOrderInput{EventID: eventID, Quantity: quantity},
	}, nil
}

func decodeJSON(payload []byte, target any, strict bool) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("multiple JSON values are not allowed")
	}
	return nil
}
