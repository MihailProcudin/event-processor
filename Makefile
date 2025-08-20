SHELL := /bin/bash
BIN_DIR := bin

.PHONY: build deploy destroy seed test lint fmt

build:
	mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $(BIN_DIR)/bootstrap ./cmd/event-processor
	cd $(BIN_DIR) && zip -q event-processor.zip bootstrap
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $(BIN_DIR)/bootstrap ./cmd/seeder
	cd $(BIN_DIR) && zip -q seeder.zip bootstrap
	rm $(BIN_DIR)/bootstrap

deploy:
	cd infra && terraform init && terraform apply -auto-approve

destroy:
	cd infra && terraform destroy -auto-approve

seed:
	aws lambda invoke --function-name event-processor-seed /dev/stdout >/dev/null

fmt:
	go fmt ./...

lint:
	golangci-lint run || true

test:
	go test ./... -v
