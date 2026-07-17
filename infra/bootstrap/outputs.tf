output "aws_account_id" {
  description = "AWS account bootstrapped for this deployment."
  value       = data.aws_caller_identity.current.account_id
}

output "state_bucket_name" {
  description = "S3 bucket used by the application Terraform backend."
  value       = aws_s3_bucket.terraform_state.bucket
}

output "hosted_zone_id" {
  description = "Route 53 hosted-zone ID for the delegated service hostname."
  value       = aws_route53_zone.service.zone_id
}

output "route53_name_servers" {
  description = "Add these four servers as Namecheap NS records with host twth."
  value       = aws_route53_zone.service.name_servers
}

output "github_deploy_role_arn" {
  description = "Set this as the GitHub repository variable AWS_ROLE_ARN."
  value       = aws_iam_role.github_deploy.arn
}
