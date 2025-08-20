package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"io/fs"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"

	embSchemas "github.com/MihailProcudin/event-processor/schemas"
)

type Request struct{}
type Response struct {
	Message string `json:"message"`
}

func getEnv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func handler(ctx context.Context, _ Request) (Response, error) {
	region := getEnv("AWS_REGION", "us-east-1")
	project := getEnv("PROJECT", "event-processor")

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return Response{}, err
	}
	sqsc := sqs.NewFromConfig(awsCfg)

	queueName := fmt.Sprintf("%s-events.fifo", project)
	qurlOut, err := sqsc.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: &queueName})
	if err != nil {
		return Response{}, fmt.Errorf("sqs:GetQueueUrl: %w", err)
	}
	if err := publishAllSamples(ctx, sqsc, *qurlOut.QueueUrl); err != nil {
		return Response{}, err
	}

	return Response{Message: "Seed Lambda completed"}, nil
}

// publishAllSamples attempts to publish one sample for each known event type
// if its corresponding schema file exists in the embedded FS.
func publishAllSamples(ctx context.Context, sqsc *sqs.Client, queueURL string) error {
	var firstErr error
	setFirstErr := func(e error) {
		if firstErr == nil && e != nil {
			firstErr = e
		}
	}
	if err := publishUserCreatedSample(ctx, sqsc, queueURL); err != nil {
		setFirstErr(err)
	}
	if err := publishMonitoringSample(ctx, sqsc, queueURL); err != nil {
		setFirstErr(err)
	}
	if err := publishUserApplicationsSample(ctx, sqsc, queueURL); err != nil {
		setFirstErr(err)
	}
	if err := publishTransactionAuthorizersSample(ctx, sqsc, queueURL); err != nil {
		setFirstErr(err)
	}
	if err := publishIntegrationsSample(ctx, sqsc, queueURL); err != nil {
		setFirstErr(err)
	}
	return firstErr
}

func hasSchema(name string) bool {
	if _, err := fs.Stat(embSchemas.FS, name); err != nil {
		return false
	}
	return true
}

func sendEvent(ctx context.Context, sqsc *sqs.Client, queueURL, tenant, eventType string, payload map[string]any) error {
	eventID := uuid.NewString()
	env := map[string]any{
		"tenant_id":  tenant,
		"event_id":   eventID,
		"event_type": eventType,
		"route":      "default",
		"payload":    payload,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(env)
	_, err := sqsc.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               &queueURL,
		MessageBody:            awsString(string(b)),
		MessageGroupId:         awsString(tenant),
		MessageDeduplicationId: awsString(eventID),
	})
	return err
}

func publishUserCreatedSample(ctx context.Context, sqsc *sqs.Client, queueURL string) error {
	if !hasSchema("user.created.json") {
		log.Printf("user.created schema missing; skipping publish")
		return nil
	}
	tenant := "tenant-a"
	payload := map[string]any{
		"id":    uuid.NewString(),
		"email": "user@example.com",
	}
	return sendEvent(ctx, sqsc, queueURL, tenant, "user.created", payload)
}

func publishMonitoringSample(ctx context.Context, sqsc *sqs.Client, queueURL string) error {
	if !hasSchema("monitoring.json") {
		log.Printf("monitoring schema missing; skipping publish")
		return nil
	}
	tenant := "tenant-a"
	payload := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"level":     "INFO",
		"message":   "seeder health check ok",
		"source":    "seeder",
		"labels": map[string]any{
			"env":    "dev",
			"region": os.Getenv("AWS_REGION"),
		},
		"metrics": map[string]any{
			"latency_ms": 12.3,
		},
	}
	return sendEvent(ctx, sqsc, queueURL, tenant, "monitoring", payload)
}

func publishUserApplicationsSample(ctx context.Context, sqsc *sqs.Client, queueURL string) error {
	if !hasSchema("user.applications.json") {
		log.Printf("user.applications schema missing; skipping publish")
		return nil
	}
	tenant := "tenant-a"
	payload := map[string]any{
		"app_id":    "app-123",
		"user_id":   uuid.NewString(),
		"action":    "install",
		"metadata":  map[string]any{"platform": "android", "version": "1.0.0"},
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	return sendEvent(ctx, sqsc, queueURL, tenant, "user.applications", payload)
}

func publishTransactionAuthorizersSample(ctx context.Context, sqsc *sqs.Client, queueURL string) error {
	if !hasSchema("transaction.authorizers.json") {
		log.Printf("transaction.authorizers schema missing; skipping publish")
		return nil
	}
	tenant := "tenant-a"
	payload := map[string]any{
		"transaction_id": uuid.NewString(),
		"authorizer":     "risk-engine",
		"decision":       "APPROVED",
		"reason":         "score high",
		"amount":         123.45,
		"currency":       "USD",
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
	}
	return sendEvent(ctx, sqsc, queueURL, tenant, "transaction.authorizers", payload)
}

func publishIntegrationsSample(ctx context.Context, sqsc *sqs.Client, queueURL string) error {
	if !hasSchema("integrations.json") {
		log.Printf("integrations schema missing; skipping publish")
		return nil
	}
	tenant := "tenant-a"
	payload := map[string]any{
		"integration_id": "int-abc",
		"provider":       "stripe",
		"event":          "webhook.received",
		"payload": map[string]any{
			"type": "invoice.paid",
			"id":   uuid.NewString(),
		},
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	return sendEvent(ctx, sqsc, queueURL, tenant, "integrations", payload)
}

func awsString(s string) *string { return &s }

func main() {
	lambda.Start(handler)
}
