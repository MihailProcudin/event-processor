# Event Processor (AWS)

A resilient, multi-tenant event ingestion pipeline on AWS. It ingests events via SQS (FIFO), enforces idempotency with DynamoDB, validates payloads against embedded JSON Schemas, and writes an event ledger to DynamoDB for auditing and queries.

## Table of Contents

- [Event Processor (AWS)](#event-processor-aws)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Prerequisites](#prerequisites)
  - [Setup and Installation](#setup-and-installation)
  - [Build](#build)
  - [Deploy Infrastructure](#deploy-infrastructure)
  - [Seed Sample Events](#seed-sample-events)
  - [Configuration](#configuration)
  - [Data Model](#data-model)
  - [Run Locally](#run-locally)
  - [Testing](#testing)
  - [Makefile Targets](#makefile-targets)
  - [Troubleshooting](#troubleshooting)

## Overview

Two AWS Lambdas are provided:

- event-processor: SQS-triggered; validates events and persists them with idempotency.
- event-processor-seed: on-demand; publishes sample events to the FIFO queue.

## Prerequisites

- Terraform >= 1.6
- Go >= 1.22 (for local builds/tests)
- AWS CLI v2
- make, zip

Authenticate with AWS first.

If you use SSO:

```bash
aws sso login --profile your-profile
export AWS_PROFILE=your-profile
aws sts get-caller-identity
```

If you use static keys (not recommended):

```bash
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export AWS_REGION=us-east-1
```

## Setup and Installation

Clone the repo and ensure dependencies are tidy:

```bash
git clone https://github.com/MihailProcudin/event-processor.git
cd event-processor
go mod tidy
```

Optional project configuration (Terraform defaults shown):

- project: event-processor
- region: us-east-1
- events_ttl_days: 0
- idempotency_ttl_days: 7

You can override via Terraform variables or `infra/variables.tf`.

## Build

Build both Lambda bundles (Linux/amd64):

```bash
make build
```

Artifacts:

- bin/event-processor.zip
- bin/seeder.zip

## Deploy Infrastructure

Provision AWS resources (DynamoDB tables, SQS FIFO + DLQ, Lambda functions):

```bash
make deploy
```

This creates:

- DynamoDB tables: ${project}-events, ${project}-idempotency
- SQS FIFO + DLQ: ${project}-events.fifo, ${project}-events-dlq.fifo
- Lambdas: ${project} (processor), ${project}-seed (seeder)

Destroy when done:

```bash
make destroy
```

Note: example uses local Terraform state. For team use, configure a remote backend (S3 + DynamoDB lock).

## Seed Sample Events

After deploy, invoke the seeder Lambda:

```bash
aws lambda invoke --function-name event-processor-seed /dev/stdout
```

The seeder publishes one sample per embedded schema file:

- user.created -> schemas/user.created.json
- monitoring -> schemas/monitoring.json
- user.applications -> schemas/user.applications.json
- transaction.authorizers -> schemas/transaction.authorizers.json
- integrations -> schemas/integrations.json

## Configuration

Terraform variables (defaults in `infra/variables.tf`):

- project (string)
- region (string)
- events_ttl_days (number)
- idempotency_ttl_days (number)

Lambda environment variables (set by Terraform):

- EVENTS_TABLE
- IDEMPOTENCY_TABLE
- EVENTS_TTL_DAYS
- IDEMPOTENCY_TTL_DAYS
- AWS_REGION (in Lambda runtime, from AWS)

## Data Model

- DynamoDB table `events`
  - PK: `tenant_id` (S)
  - SK: `sk` (S) = `created_at#event_id`
  - Attributes: `event_type` (S), `status` (S: READY|INVALID), `route` (S), `payload` (M), `retries` (N), `reason` (S?), `ttl` (N), `tenant_id_created_at` (S)
  - GSI: `gsi_status_tenant` — PK `status`, SK `tenant_id_created_at`
  - TTL: enabled on `ttl`
  - Streams: NEW_IMAGE

- DynamoDB table `idempotency`
  - PK: `pk` (S) = `tenant_id#event_id`
  - TTL: enabled on `ttl`

## Run Locally

Local execution is not supported. The processor runs in AWS as an SQS-triggered Lambda, and the seeder is an on-demand Lambda. Use the deploy and seed steps to test.

## Testing

Run unit tests:

```bash
go test ./cmd/event-processor -v
```

## Makefile Targets

- build: builds both zips into bin/
- deploy: terraform init + apply in infra/
- destroy: terraform destroy in infra/
- seed: invoke seeder Lambda in AWS
- fmt: go fmt ./...
- lint: golangci-lint run (non-fatal)
- test: go test ./... -v

## Troubleshooting

- Ensure AWS credentials are set (SSO or static keys). `terraform` and AWS CLI use your current profile.
- SQS FIFO requires MessageGroupId; the seeder uses tenant_id automatically.
- Consider increasing SQS `visibility_timeout_seconds` to at least 6× the Lambda timeout.
