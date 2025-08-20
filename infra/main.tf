locals {
  name = var.project
}

resource "aws_dynamodb_table" "events" {
  name         = "${local.name}-events"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "tenant_id"
  range_key    = "sk"

  attribute {
    name = "tenant_id"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "S"
  }
  attribute {
    name = "status"
    type = "S"
  }
  attribute {
    name = "tenant_id_created_at"
    type = "S"
  }

  global_secondary_index {
    name            = "gsi_status_tenant"
    hash_key        = "status"
    range_key       = "tenant_id_created_at"
    projection_type = "ALL"
  }

  point_in_time_recovery {
    enabled = false
  }
  stream_enabled   = true
  stream_view_type = "NEW_IMAGE"

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }
}

resource "aws_dynamodb_table" "idempotency" {
  name         = "${local.name}-idempotency"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  attribute {
    name = "pk"
    type = "S"
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }
}

resource "aws_sqs_queue" "events_fifo_dlq" {
  name                        = "${local.name}-events-dlq.fifo"
  fifo_queue                  = true
  content_based_deduplication = true
}

resource "aws_sqs_queue" "events_fifo" {
  name                        = "${local.name}-events.fifo"
  fifo_queue                  = true
  content_based_deduplication = true
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.events_fifo_dlq.arn
    maxReceiveCount     = 5
  })
}

# Lambda IAM
resource "aws_iam_role" "lambda_role" {
  name = "${local.name}-lambda-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Action    = "sts:AssumeRole",
      Effect    = "Allow",
      Principal = { Service = "lambda.amazonaws.com" }
    }]
  })
}

resource "aws_iam_role_policy" "lambda_policy" {
  role = aws_iam_role.lambda_role.id
  policy = jsonencode({
    Version = "2012-10-17",
    Statement = [
      { Effect = "Allow", Action = [
  "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:GetItem", "dynamodb:Query", "dynamodb:TransactWriteItems"
      ], Resource = [aws_dynamodb_table.events.arn, aws_dynamodb_table.idempotency.arn, "${aws_dynamodb_table.events.arn}/index/*"] },
      { Effect = "Allow", Action = [
        "sqs:ReceiveMessage",
        "sqs:DeleteMessage",
        "sqs:GetQueueAttributes",
        "sqs:GetQueueUrl",
        "sqs:ChangeMessageVisibility",
        "sqs:SendMessage"
      ], Resource = aws_sqs_queue.events_fifo.arn },
      { Effect = "Allow", Action = ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"], Resource = "*" }
    ]
  })
}

resource "aws_lambda_function" "processor" {
  function_name    = local.name
  role             = aws_iam_role.lambda_role.arn
  handler          = "bootstrap"
  runtime          = "provided.al2"
  filename         = "../bin/event-processor.zip"
  source_code_hash = filebase64sha256("../bin/event-processor.zip")
  timeout          = 15
  memory_size      = 256

  environment {
    variables = {
      EVENTS_TABLE         = aws_dynamodb_table.events.name
      IDEMPOTENCY_TABLE    = aws_dynamodb_table.idempotency.name
      EVENTS_TTL_DAYS      = tostring(var.events_ttl_days)
      IDEMPOTENCY_TTL_DAYS = tostring(var.idempotency_ttl_days)
    }
  }
}

resource "aws_lambda_event_source_mapping" "sqs_trigger" {
  event_source_arn = aws_sqs_queue.events_fifo.arn
  function_name    = aws_lambda_function.processor.arn
  enabled          = true
  batch_size       = 1
  visibility_timeout_seconds = 30
}

# Seed Lambda to upload schemas and publish a test event when invoked
resource "aws_lambda_function" "seed" {
  function_name    = "${local.name}-seed"
  role             = aws_iam_role.lambda_role.arn
  handler          = "bootstrap"
  runtime          = "provided.al2"
  filename         = "../bin/seeder.zip"
  source_code_hash = filebase64sha256("../bin/seeder.zip")
  timeout          = 30
  memory_size      = 256

  environment {
    variables = {
      PROJECT = var.project
    }
  }
}
