package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

// valid user
func TestValidate_UserCreatedValid(t *testing.T) {
	env := Envelope{
		TenantID:  "t1",
		EventID:   "e1",
		EventType: "user.created",
		Payload: map[string]any{
			"id":    "123",
			"email": "user@example.com",
		},
	}
	ok, reason := validate(env)
	if !ok {
		t.Fatalf("expected valid, got invalid: %s", reason)
	}
}

// user missing email
func TestValidate_UserCreatedInvalid(t *testing.T) {

	env := Envelope{
		TenantID:  "t1",
		EventID:   "e1",
		EventType: "user.created",
		Payload:   map[string]any{"id": "123"},
	}
	ok, reason := validate(env)
	if ok {
		t.Fatalf("expected invalid, got valid")
	}
	if reason == "" {
		t.Fatalf("expected reason for invalid schema")
	}
}

func TestToAttr_BasicTypes(t *testing.T) {
	cases := []any{"s", true, float64(1.2), 42, []any{"a", 1}, map[string]any{"k": "v"}, nil}
	for _, c := range cases {
		if av := toAttr(c); av == nil {
			t.Fatalf("attribute value is nil for %T", c)
		}
	}
}

func TestHandler_DuplicateAck(t *testing.T) {
	t.Cleanup(func() { writeEventOnce = persistEventOnce })
	writeEventOnce = func(ctx context.Context, env Envelope, status, reason string) (bool, error) {
		return false, nil
	}

	body := map[string]any{
		"tenant_id":  "t1",
		"event_id":   "e1",
		"event_type": "user.created",
		"payload": map[string]any{
			"id":    "123",
			"email": "user@example.com",
		},
	}
	b, _ := json.Marshal(body)
	rec := events.SQSMessage{Body: string(b)}

	if err := handleRecord(context.Background(), rec); err != nil {
		t.Fatalf("expected nil error for duplicate ack, got %v", err)
	}
}
