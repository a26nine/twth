output "service_url" {
  description = "Public HTTPS URL for the RPC proxy."
  value       = "https://${var.public_hostname}"
}

output "certificate_arn" {
  description = "Validated ACM certificate ARN used by the HTTPS listener."
  value       = aws_acm_certificate_validation.this.certificate_arn
}

output "waf_web_acl_arn" {
  description = "Regional WAFv2 web ACL ARN associated with the ALB."
  value       = aws_wafv2_web_acl.this.arn
}

output "route53_zone_id" {
  description = "Delegated Route 53 hosted-zone ID."
  value       = data.aws_route53_zone.service.zone_id
}

output "vpc_id" {
  description = "Application VPC ID."
  value       = aws_vpc.this.id
}

output "public_subnet_ids" {
  description = "Public subnet IDs used by the ALB and Fargate tasks."
  value       = aws_subnet.public[*].id
}

output "alb_security_group_id" {
  description = "Application Load Balancer security group ID."
  value       = aws_security_group.alb.id
}

output "task_security_group_id" {
  description = "Fargate task security group ID."
  value       = aws_security_group.task.id
}

output "ecs_cluster_name" {
  description = "ECS cluster name."
  value       = aws_ecs_cluster.this.name
}

output "ecs_cluster_arn" {
  description = "ECS cluster ARN."
  value       = aws_ecs_cluster.this.arn
}

output "ecs_service_name" {
  description = "ECS service name."
  value       = aws_ecs_service.this.name
}

output "ecs_service_id" {
  description = "ECS service identifier."
  value       = aws_ecs_service.this.id
}

output "alb_dns_name" {
  description = "Application Load Balancer DNS name."
  value       = aws_lb.this.dns_name
}

output "alb_arn" {
  description = "Application Load Balancer ARN."
  value       = aws_lb.this.arn
}

output "target_group_arn" {
  description = "ALB target group ARN used for ECS task health checks."
  value       = aws_lb_target_group.this.arn
}
