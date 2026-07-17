variable "aws_region" {
  description = "AWS region used for regional bootstrap API calls."
  type        = string
  default     = "us-east-1"

  validation {
    condition     = trimspace(var.aws_region) != ""
    error_message = "aws_region must not be empty."
  }
}

variable "name" {
  description = "Short project name used for bootstrap resources."
  type        = string
  default     = "twth"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,22}[a-z0-9]$", var.name))
    error_message = "name must be 3-24 lowercase alphanumeric or hyphen characters."
  }
}

variable "public_hostname" {
  description = "Delegated public DNS zone used by the service."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$", var.public_hostname))
    error_message = "public_hostname must be a lowercase fully qualified hostname without a trailing dot."
  }
}

variable "github_owner" {
  description = "GitHub repository owner."
  type        = string
}

variable "github_owner_id" {
  description = "Immutable GitHub repository-owner ID."
  type        = number
}

variable "github_repository" {
  description = "GitHub repository name."
  type        = string
}

variable "github_repository_id" {
  description = "Immutable GitHub repository ID."
  type        = number
}

variable "github_branch" {
  description = "Only this branch may assume the AWS deploy role."
  type        = string
  default     = "main"
}
