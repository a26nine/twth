mock_provider "aws" {
  override_during = plan

  mock_data "aws_availability_zones" {
    defaults = {
      names = ["us-east-1a", "us-east-1b"]
    }
  }

  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
      arn        = "arn:aws:iam::123456789012:role/test"
      user_id    = "AROATEST"
    }
  }

  mock_data "aws_route53_zone" {
    defaults = {
      arn     = "arn:aws:route53:::hostedzone/Z0123456789"
      name    = "rpc.example.com"
      zone_id = "Z0123456789"
    }
  }

  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"Service\":\"ecs-tasks.amazonaws.com\"},\"Action\":\"sts:AssumeRole\"}]}"
    }
  }

  mock_resource "aws_vpc" {
    defaults = {
      id = "vpc-00000000000000000"
    }
  }

  mock_resource "aws_security_group" {
    defaults = {
      id = "sg-00000000000000001"
    }
  }

  mock_resource "aws_cloudwatch_log_group" {
    defaults = {
      arn = "arn:aws:logs:us-east-1:123456789012:log-group:/ecs/polygon-rpc-proxy"
    }
  }
}

run "mocked_foundation_plan" {
  command = plan

  variables {
    aws_account_id  = "123456789012"
    image_digest    = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    public_hostname = "rpc.example.com"
  }

  assert {
    condition = (
      aws_vpc.this.cidr_block == "10.20.0.0/16" &&
      aws_vpc.this.enable_dns_support &&
      aws_vpc.this.enable_dns_hostnames
    )
    error_message = "The VPC defaults are incorrect."
  }

  assert {
    condition = (
      length(aws_subnet.public) == 2 &&
      aws_subnet.public[0].availability_zone == "us-east-1a" &&
      aws_subnet.public[1].availability_zone == "us-east-1b" &&
      aws_subnet.public[0].map_public_ip_on_launch &&
      aws_subnet.public[1].map_public_ip_on_launch
    )
    error_message = "Two public subnets across two AZs are required."
  }

  assert {
    condition = (
      aws_vpc_security_group_ingress_rule.alb_http.from_port == 80 &&
      aws_vpc_security_group_ingress_rule.alb_https.from_port == 443 &&
      aws_vpc_security_group_egress_rule.alb_to_tasks.to_port == 8080 &&
      aws_vpc_security_group_ingress_rule.task_from_alb.from_port == 8080 &&
      aws_vpc_security_group_egress_rule.task_https.to_port == 443
    )
    error_message = "Real AWS must use only modern standalone security-group rule resources."
  }

  assert {
    condition = (
      toset(jsondecode(aws_iam_role_policy.ecs_task_execution_logs.policy).Statement[0].Action) == toset([
        "logs:CreateLogStream",
        "logs:PutLogEvents",
      ]) &&
      jsondecode(aws_iam_role_policy.ecs_task_execution_logs.policy).Statement[0].Resource == "arn:aws:logs:us-east-1:123456789012:log-group:/ecs/polygon-rpc-proxy:*"
    )
    error_message = "The task execution role must write only to the application log group."
  }

  assert {
    condition = (
      data.aws_route53_zone.service.name == "rpc.example.com" &&
      data.aws_route53_zone.service.private_zone == false
    )
    error_message = "The application must use the delegated public Route 53 zone."
  }
}

run "rejects_wrong_account" {
  command = plan

  variables {
    aws_account_id  = "999999999999"
    image_digest    = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    public_hostname = "rpc.example.com"
  }

  expect_failures = [aws_vpc.this]
}
