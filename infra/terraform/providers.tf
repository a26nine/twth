provider "aws" {
  region              = var.aws_region
  allowed_account_ids = [var.aws_account_id]

  default_tags {
    tags = merge(var.tags, {
      ManagedBy = "Terraform"
      Project   = var.name
    })
  }
}

data "aws_caller_identity" "current" {}
