variable "project" {
  type    = string
  default = "event-processor"
}

variable "region" {
  type    = string
  default = "us-east-1"
}

variable "events_ttl_days" {
  type    = number
  default = 0
}

variable "idempotency_ttl_days" {
  type    = number
  default = 7
}
