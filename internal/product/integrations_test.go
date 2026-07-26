package product

import (
	"errors"
	"testing"
)

// TestParseStripeOrder validates supported checkout mapping and ignores unrelated Stripe event types.
func TestParseStripeOrder(t *testing.T) {
	payload := []byte(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_1","client_reference_id":"show-42","metadata":{"quantity":"3"}}}}`)
	order, err := ParseStripeOrder(payload)
	if err != nil {
		t.Fatalf("parse Stripe order: %v", err)
	}
	if order.IdempotencyKey != "stripe:evt_1" || order.ExternalID != "cs_1" || order.Input.EventID != "show-42" || order.Input.Quantity != 3 {
		t.Fatalf("unexpected Stripe mapping: %+v", order)
	}

	_, err = ParseStripeOrder([]byte(`{"id":"evt_2","type":"charge.refunded","data":{"object":{}}}`))
	if !errors.Is(err, ErrIgnoredEvent) {
		t.Fatalf("expected ignored event, got %v", err)
	}
}

// TestParseShopifyOrder validates order identity, event metadata, and quantity aggregation across line items.
func TestParseShopifyOrder(t *testing.T) {
	payload := []byte(`{"id":98765,"line_items":[{"quantity":2},{"quantity":1}],"note_attributes":[{"name":"event_id","value":"show-42"}]}`)
	order, err := ParseShopifyOrder(payload)
	if err != nil {
		t.Fatalf("parse Shopify order: %v", err)
	}
	if order.IdempotencyKey != "shopify:98765" || order.ExternalID != "98765" || order.Input.EventID != "show-42" || order.Input.Quantity != 3 {
		t.Fatalf("unexpected Shopify mapping: %+v", order)
	}
}
