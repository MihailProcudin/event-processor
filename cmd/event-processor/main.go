// Package main implements the event-processor AWS Lambda function.
// 1. It consumes events from SQS and enforces idempotency using DynamoDB,
// 2. Validates payloads against embedded JSON Schemas (schemas/<event_type>.json)
// 3. Persists an event ledger to DynamoDB.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	dynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynatypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/xeipuuv/gojsonschema"

	embschemas "github.com/MihailProcudin/event-processor/schemas"
)

// AppConfig holds runtime configuration loaded from environment variables and
// used by the Lambda function.
type AppConfig struct {
	EventsTable   string
	IdemTable     string
	Region        string
	EventsTTLDays int
	IdemTTLDays   int
}

// SchemaCache caches compiled JSON Schemas by event type to avoid recompiling
// on every invocation.
type SchemaCache struct {
	mu     sync.RWMutex
	byType map[string]*gojsonschema.Schema
}

var (
	dbClient *dynamodb.Client
	cfg      AppConfig
	schemas  = SchemaCache{byType: make(map[string]*gojsonschema.Schema)}
)

// writeEventOnce is an indirection for persistEventOnce to enable stubbing in tests.
var writeEventOnce = persistEventOnce

// Envelope describes the event contract consumed from SQS. The payload is
// validated against an embedded JSON Schema identified by EventType.
type Envelope struct {
	TenantID  string         `json:"tenant_id"`
	EventID   string         `json:"event_id"`
	EventType string         `json:"event_type"`
	Route     string         `json:"route,omitempty"` // optional routing key
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"created_at,omitempty"`
}

// getEnv returns the value of key or a default if unset.
func getEnv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

// mustInit initializes AWS SDK clients and runtime configuration. It panics the
// process (via log.Fatalf) if the AWS config cannot be loaded.
func mustInit(ctx context.Context) {
	cfg.Region = getEnv("AWS_REGION", "us-east-1")
	cfg.EventsTable = os.Getenv("EVENTS_TABLE")
	cfg.IdemTable = os.Getenv("IDEMPOTENCY_TABLE")
	if d := os.Getenv("EVENTS_TTL_DAYS"); d != "" {
		if n, err := strconv.Atoi(d); err == nil {
			cfg.EventsTTLDays = n
		}
	}
	if d := os.Getenv("IDEMPOTENCY_TTL_DAYS"); d != "" {
		if n, err := strconv.Atoi(d); err == nil {
			cfg.IdemTTLDays = n
		}
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		log.Fatalf("config load: %v", err)
	}
	dbClient = dynamodb.NewFromConfig(awsCfg)
}

// ensureIdempotent records a (tenantID,eventID) pair in the idempotency table.
// It returns true if this is the first time the pair is seen, or false if the
// item already existed (duplicate/retry). Any unexpected DynamoDB error is
// returned.
// persistEventOnce writes both the idempotency record and the event item atomically.
// Returns (true, nil) when newly written, (false, nil) when a duplicate, or (false, err) on other errors.
func persistEventOnce(ctx context.Context, env Envelope, status string, reason string) (bool, error) {
	// Build idempotency item
	pk := env.TenantID + "#" + env.EventID
	idem := map[string]dynatypes.AttributeValue{
		"pk":         &dynatypes.AttributeValueMemberS{Value: pk},
		"created_at": &dynatypes.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339Nano)},
		"event_type": &dynatypes.AttributeValueMemberS{Value: env.EventType},
	}
	if cfg.IdemTTLDays > 0 {
		exp := time.Now().UTC().Add(time.Duration(cfg.IdemTTLDays) * 24 * time.Hour).Unix()
		idem["ttl"] = &dynatypes.AttributeValueMemberN{Value: fmt.Sprintf("%d", exp)}
	}

	// Build event item
	created := env.CreatedAt
	if created == "" {
		created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	sk := fmt.Sprintf("%s#%s", created, env.EventID)
	evt := map[string]dynatypes.AttributeValue{
		"tenant_id":            &dynatypes.AttributeValueMemberS{Value: env.TenantID},
		"sk":                   &dynatypes.AttributeValueMemberS{Value: sk},
		"event_type":           &dynatypes.AttributeValueMemberS{Value: env.EventType},
		"status":               &dynatypes.AttributeValueMemberS{Value: status},
		"payload":              &dynatypes.AttributeValueMemberM{Value: toAttrMap(env.Payload)},
		"retries":              &dynatypes.AttributeValueMemberN{Value: "0"},
		"tenant_id_created_at": &dynatypes.AttributeValueMemberS{Value: env.TenantID + "#" + created},
	}
	if env.Route != "" {
		evt["route"] = &dynatypes.AttributeValueMemberS{Value: env.Route}
	}
	if reason != "" {
		evt["reason"] = &dynatypes.AttributeValueMemberS{Value: reason}
	}
	if cfg.EventsTTLDays > 0 {
		exp := time.Now().UTC().Add(time.Duration(cfg.EventsTTLDays) * 24 * time.Hour).Unix()
		evt["ttl"] = &dynatypes.AttributeValueMemberN{Value: fmt.Sprintf("%d", exp)}
	}

	attrNotExists := func(attr string) *string { s := fmt.Sprintf("attribute_not_exists(%s)", attr); return &s }
	input := &dynamodb.TransactWriteItemsInput{
		TransactItems: []dynatypes.TransactWriteItem{
			{
				Put: &dynatypes.Put{
					TableName:           &cfg.IdemTable,
					Item:                idem,
					ConditionExpression: attrNotExists("pk"),
				},
			},
			{
				Put: &dynatypes.Put{
					TableName:           &cfg.EventsTable,
					Item:                evt,
					ConditionExpression: attrNotExists("sk"),
				},
			},
		},
	}
	_, err := dbClient.TransactWriteItems(ctx, input)
	if err != nil {
		// Detect duplicate via conditional failure
		var tce *dynatypes.TransactionCanceledException
		if errors.As(err, &tce) {
			// If any reason is ConditionalCheckFailed -> treat as duplicate
			for _, r := range tce.CancellationReasons {
				if r.Code != nil && *r.Code == "ConditionalCheckFailed" {
					return false, nil
				}
			}
		}
		// Also handle simple conditional error (non-transactional fallbacks)
		var cfe *dynatypes.ConditionalCheckFailedException
		if errors.As(err, &cfe) || strings.Contains(err.Error(), "ConditionalCheckFailed") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// loadSchema retrieves and compiles the JSON Schema for the given event type
// from the embedded schemas filesystem and caches the result.
func loadSchema(eventType string) (*gojsonschema.Schema, error) {
	schemas.mu.RLock()
	if s, ok := schemas.byType[eventType]; ok {
		schemas.mu.RUnlock()
		return s, nil
	}
	schemas.mu.RUnlock()
	// Load from embedded schemas filesystem
	file := fmt.Sprintf("%s.json", eventType)
	b, err := embschemas.FS.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("embed read schema %s: %w", file, err)
	}
	loader := gojsonschema.NewBytesLoader(b)
	schema, err := gojsonschema.NewSchema(loader)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	schemas.mu.Lock()
	schemas.byType[eventType] = schema
	schemas.mu.Unlock()
	return schema, nil
}

// validate checks the Envelope payload against its JSON Schema. It returns a
// boolean indicating validity and a human-readable reason when invalid.
func validate(env Envelope) (bool, string) {
	schema, err := loadSchema(env.EventType)
	if err != nil {
		return false, fmt.Sprintf("schema load error: %v", err)
	}
	payloadLoader := gojsonschema.NewGoLoader(env.Payload)
	res, err := schema.Validate(payloadLoader)
	if err != nil {
		return false, err.Error()
	}
	if !res.Valid() {
		var msgs []string
		for _, e := range res.Errors() {
			msgs = append(msgs, e.String())
		}
		return false, strings.Join(msgs, "; ")
	}
	return true, ""
}

// putEvent persists the event to the events DynamoDB table with the provided
// processing status and optional failure reason.
// putEvent removed; handled by persistEventOnce

// toAttrMap converts a Go map[string]any to a DynamoDB attribute map.
func toAttrMap(m map[string]any) map[string]dynatypes.AttributeValue {
	out := make(map[string]dynatypes.AttributeValue, len(m))
	for k, v := range m {
		out[k] = toAttr(v)
	}
	return out
}

// toAttr converts an arbitrary Go value into a DynamoDB AttributeValue.
func toAttr(v any) dynatypes.AttributeValue {
	switch t := v.(type) {
	case nil:
		return &dynatypes.AttributeValueMemberNULL{Value: true}
	case string:
		return &dynatypes.AttributeValueMemberS{Value: t}
	case bool:
		return &dynatypes.AttributeValueMemberBOOL{Value: t}
	case float64:
		return &dynatypes.AttributeValueMemberN{Value: fmt.Sprintf("%v", t)}
	case int, int64, int32:
		return &dynatypes.AttributeValueMemberN{Value: fmt.Sprintf("%v", t)}
	case []any:
		arr := make([]dynatypes.AttributeValue, 0, len(t))
		for _, e := range t {
			arr = append(arr, toAttr(e))
		}
		return &dynatypes.AttributeValueMemberL{Value: arr}
	case map[string]any:
		return &dynatypes.AttributeValueMemberM{Value: toAttrMap(t)}
	default:
		b, _ := json.Marshal(t)
		return &dynatypes.AttributeValueMemberS{Value: string(b)}
	}
}

// handleRecord processes a single SQS message: it parses the envelope, checks
// idempotency, validates the payload, and persists the event.
func handleRecord(ctx context.Context, rec events.SQSMessage) error {
	var env Envelope
	if err := json.Unmarshal([]byte(rec.Body), &env); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	fmt.Printf("env: %+v\n", env)
	if env.TenantID == "" || env.EventID == "" || env.EventType == "" {
		return fmt.Errorf("missing required envelope fields")
	}
	valid, reason := validate(env)
	status := "READY"
	if !valid {
		status = "INVALID"
	}
	written, err := writeEventOnce(ctx, env, status, reason)
	if err != nil {
		return fmt.Errorf("persist error: %w", err)
	}
	if !written {
		// already processed — ack
		log.Printf("duplicate event skipped tenant_id=%s event_id=%s type=%s", env.TenantID, env.EventID, env.EventType)
		return nil
	}
	if status == "INVALID" {
		log.Printf("processed event status=%s reason=%q tenant_id=%s event_id=%s type=%s", status, reason, env.TenantID, env.EventID, env.EventType)
	} else {
		log.Printf("processed event status=%s tenant_id=%s event_id=%s type=%s", status, env.TenantID, env.EventID, env.EventType)
	}
	return nil
}

// handler is the AWS Lambda entry point for SQS-triggered invocations.
func handler(ctx context.Context, ev events.SQSEvent) error {
	for _, rec := range ev.Records {
		if err := handleRecord(ctx, rec); err != nil {
			log.Printf("record failed: %v", err)
			return err
		}
	}
	return nil
}

func main() {
	ctx := context.Background()
	mustInit(ctx)
	lambda.StartWithOptions(handler, lambda.WithContext(ctx))
}
