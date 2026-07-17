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

  mock_resource "aws_lb" {
    defaults = {
      arn      = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/test/123"
      dns_name = "test.us-east-1.elb.amazonaws.com"
      zone_id  = "Z35SXDOTRQ7X7K"
    }
  }

  mock_resource "aws_lb_target_group" {
    defaults = {
      arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/test/123"
    }
  }

  mock_resource "aws_acm_certificate" {
    defaults = {
      arn = "arn:aws:acm:us-east-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"
      domain_validation_options = [
        {
          domain_name           = "rpc.example.com"
          resource_record_name  = "_validation.rpc.example.com"
          resource_record_type  = "CNAME"
          resource_record_value = "_validation.acm-validations.aws"
        },
      ]
    }
  }

  mock_resource "aws_acm_certificate_validation" {
    defaults = {
      certificate_arn = "arn:aws:acm:us-east-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"
    }
  }

  mock_resource "aws_wafv2_web_acl" {
    defaults = {
      arn = "arn:aws:wafv2:us-east-1:123456789012:regional/webacl/test/123"
    }
  }
}

run "secure_https_service" {
  command = plan

  variables {
    aws_account_id  = "123456789012"
    image_digest    = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    public_hostname = "rpc.example.com"
  }

  assert {
    condition = (
      aws_acm_certificate.this.domain_name == "rpc.example.com" &&
      aws_acm_certificate.this.validation_method == "DNS"
    )
    error_message = "ACM must issue a DNS-validated service certificate."
  }

  assert {
    condition = (
      aws_lb_listener.http.default_action[0].redirect[0].protocol == "HTTPS" &&
      aws_lb_listener.http.default_action[0].redirect[0].port == "443" &&
      aws_lb_listener.http.default_action[0].redirect[0].status_code == "HTTP_301"
    )
    error_message = "HTTP must redirect permanently to HTTPS."
  }

  assert {
    condition = (
      aws_lb_listener.https.protocol == "HTTPS" &&
      aws_lb_listener.https.ssl_policy == "ELBSecurityPolicy-TLS13-1-2-2021-06" &&
      aws_lb_listener.https.routing_http_response_strict_transport_security_header_value == "max-age=63072000; includeSubDomains; preload" &&
      aws_lb_listener.https.routing_http_response_server_enabled == false
    )
    error_message = "The HTTPS listener security settings are incomplete."
  }

  assert {
    condition = (
      aws_route53_record.service.name == "rpc.example.com" &&
      aws_route53_record.service.type == "A" &&
      aws_route53_record.service.alias[0].name == aws_lb.this.dns_name
    )
    error_message = "Route 53 must alias the public hostname to the ALB."
  }

  assert {
    condition = (
      aws_wafv2_web_acl.this.scope == "REGIONAL" &&
      one(aws_wafv2_web_acl.this.rule).statement[0].rate_based_statement[0].limit == 600 &&
      one(aws_wafv2_web_acl.this.rule).statement[0].rate_based_statement[0].evaluation_window_sec == 300 &&
      one(aws_wafv2_web_acl.this.rule).action[0].block[0].custom_response[0].response_code == 429 &&
      aws_wafv2_web_acl_association.alb.resource_arn == aws_lb.this.arn
    )
    error_message = "WAF must rate-limit the ALB and return HTTP 429."
  }

  assert {
    condition = (
      aws_ecs_service.this.desired_count == 2 &&
      aws_appautoscaling_target.ecs.min_capacity == 2 &&
      aws_appautoscaling_target.ecs.max_capacity == 4
    )
    error_message = "The service must run two tasks and scale to four."
  }

  assert {
    condition = one([
      for container in jsondecode(aws_ecs_task_definition.this.container_definitions) : container.image
      if container.name == "rpc-proxy"
    ]) == "ghcr.io/a26nine/twth-rpc-proxy@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    error_message = "ECS must deploy the digest-pinned public GHCR image."
  }

  assert {
    condition     = var.upstream_url == "https://polygon.drpc.org"
    error_message = "The deployed service must use the verified public Polygon upstream by default."
  }

  assert {
    condition     = aws_ecs_service.this.network_configuration[0].assign_public_ip
    error_message = "Fargate tasks must receive public IPs for HTTPS egress."
  }

  assert {
    condition     = length(aws_subnet.public) == 2
    error_message = "Fargate must run in two subnets."
  }

  assert {
    condition = (
      aws_lb_target_group.this.target_type == "ip" &&
      aws_lb_target_group.this.protocol_version == "HTTP1"
    )
    error_message = "Fargate networking and target registration are incorrect."
  }

  assert {
    condition = (
      output.service_url == "https://rpc.example.com" &&
      output.certificate_arn == aws_acm_certificate.this.arn &&
      output.waf_web_acl_arn == aws_wafv2_web_acl.this.arn
    )
    error_message = "Public service outputs are incomplete."
  }
}

run "rejects_mutable_image_reference" {
  command = plan

  variables {
    aws_account_id  = "123456789012"
    image_digest    = "latest"
    public_hostname = "rpc.example.com"
  }

  expect_failures = [var.image_digest]
}

run "rejects_invalid_rate_window" {
  command = plan

  variables {
    aws_account_id                = "123456789012"
    image_digest                  = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    public_hostname               = "rpc.example.com"
    waf_evaluation_window_seconds = 30
  }

  expect_failures = [var.waf_evaluation_window_seconds]
}

run "rejects_capacity_order" {
  command = plan

  variables {
    aws_account_id           = "123456789012"
    image_digest             = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    public_hostname          = "rpc.example.com"
    desired_count            = 5
    autoscaling_min_capacity = 2
    autoscaling_max_capacity = 4
  }

  expect_failures = [aws_appautoscaling_target.ecs]
}
