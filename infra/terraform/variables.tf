variable "name" {
  description = "Name prefix for application resources."
  type        = string
  default     = "polygon-rpc-proxy"
}

variable "aws_account_id" {
  description = "Expected AWS account ID. Terraform refuses to operate in another account."
  type        = string

  validation {
    condition     = can(regex("^[0-9]{12}$", var.aws_account_id))
    error_message = "aws_account_id must contain exactly 12 digits."
  }
}

variable "aws_region" {
  description = "AWS region in which to deploy the application."
  type        = string
  default     = "us-east-1"
}

variable "public_hostname" {
  description = "Public DNS hostname for the proxy."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$", var.public_hostname))
    error_message = "public_hostname must be a valid lowercase fully qualified domain name."
  }
}

variable "vpc_cidr" {
  description = "IPv4 CIDR block for the application VPC."
  type        = string
  default     = "10.20.0.0/16"
}

variable "public_subnet_cidrs" {
  description = "IPv4 CIDR blocks for the two public subnets."
  type        = list(string)
  default     = ["10.20.0.0/24", "10.20.1.0/24"]

  validation {
    condition     = length(var.public_subnet_cidrs) == 2
    error_message = "Exactly two public subnet CIDRs are required."
  }
}

variable "image_digest" {
  description = "Immutable SHA-256 digest of the public GHCR release image to deploy."
  type        = string

  validation {
    condition     = can(regex("^sha256:[0-9a-f]{64}$", var.image_digest))
    error_message = "image_digest must be sha256 followed by exactly 64 lowercase hexadecimal characters."
  }
}

variable "desired_count" {
  description = "Desired number of running ECS tasks."
  type        = number
  default     = 2

  validation {
    condition     = var.desired_count >= 1 && floor(var.desired_count) == var.desired_count
    error_message = "desired_count must be a positive integer."
  }
}

variable "autoscaling_min_capacity" {
  description = "Minimum ECS task count."
  type        = number
  default     = 2

  validation {
    condition     = var.autoscaling_min_capacity >= 1 && floor(var.autoscaling_min_capacity) == var.autoscaling_min_capacity
    error_message = "autoscaling_min_capacity must be a positive integer."
  }
}

variable "autoscaling_max_capacity" {
  description = "Maximum ECS task count."
  type        = number
  default     = 4

  validation {
    condition     = var.autoscaling_max_capacity >= 1 && floor(var.autoscaling_max_capacity) == var.autoscaling_max_capacity
    error_message = "autoscaling_max_capacity must be a positive integer."
  }
}

variable "autoscaling_cpu_target" {
  description = "Average CPU utilization percentage targeted by ECS autoscaling."
  type        = number
  default     = 70

  validation {
    condition     = var.autoscaling_cpu_target > 0 && var.autoscaling_cpu_target <= 100
    error_message = "autoscaling_cpu_target must be greater than 0 and no more than 100."
  }
}

variable "task_cpu" {
  description = "Fargate task CPU units."
  type        = number
  default     = 256
}

variable "task_memory" {
  description = "Fargate task memory in MiB."
  type        = number
  default     = 512
}

variable "upstream_url" {
  description = "HTTPS Polygon JSON-RPC upstream URL."
  type        = string
  default     = "https://polygon.drpc.org"

  validation {
    condition     = startswith(var.upstream_url, "https://")
    error_message = "upstream_url must use HTTPS."
  }
}

variable "health_check_interval_seconds" {
  description = "ALB target health-check interval."
  type        = number
  default     = 30
}

variable "health_check_timeout_seconds" {
  description = "ALB target health-check timeout."
  type        = number
  default     = 5
}

variable "healthy_threshold_count" {
  description = "Consecutive successful health checks before a target becomes healthy."
  type        = number
  default     = 2
}

variable "unhealthy_threshold_count" {
  description = "Consecutive failed health checks before a target becomes unhealthy."
  type        = number
  default     = 3
}

variable "log_retention_days" {
  description = "CloudWatch Logs retention period."
  type        = number
  default     = 14
}

variable "waf_rate_limit" {
  description = "Maximum requests allowed from one IP during the WAF evaluation window."
  type        = number
  default     = 600

  validation {
    condition     = var.waf_rate_limit >= 10 && floor(var.waf_rate_limit) == var.waf_rate_limit
    error_message = "waf_rate_limit must be an integer of at least 10."
  }
}

variable "waf_evaluation_window_seconds" {
  description = "WAF rate-limit evaluation window in seconds."
  type        = number
  default     = 300

  validation {
    condition     = contains([60, 120, 300, 600], var.waf_evaluation_window_seconds)
    error_message = "waf_evaluation_window_seconds must be 60, 120, 300, or 600."
  }
}

variable "tags" {
  description = "Additional tags for all resources."
  type        = map(string)
  default     = {}
}
