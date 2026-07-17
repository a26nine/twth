mock_provider "aws" {
  override_during = plan

  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
      arn        = "arn:aws:iam::123456789012:user/bootstrap"
      user_id    = "AIDATEST"
    }
  }

  mock_resource "aws_s3_bucket" {
    defaults = {
      arn    = "arn:aws:s3:::twth-terraform-state-123456789012"
      bucket = "twth-terraform-state-123456789012"
      id     = "twth-terraform-state-123456789012"
    }
  }

  mock_resource "aws_route53_zone" {
    defaults = {
      arn = "arn:aws:route53:::hostedzone/Z0123456789"
      name_servers = [
        "ns-1.awsdns-00.com",
        "ns-2.awsdns-00.net",
        "ns-3.awsdns-00.org",
        "ns-4.awsdns-00.co.uk",
      ]
      zone_id = "Z0123456789"
    }
  }

  mock_resource "aws_iam_openid_connect_provider" {
    defaults = {
      arn             = "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com"
      thumbprint_list = []
    }
  }

  mock_resource "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::123456789012:role/twth-github-deploy"
    }
  }
}

run "secure_bootstrap_defaults" {
  command = plan

  variables {
    public_hostname      = "rpc.example.com"
    github_owner         = "example-org"
    github_owner_id      = 11111111
    github_repository    = "example-repository"
    github_repository_id = 22222222
  }

  assert {
    condition     = aws_s3_bucket.terraform_state.bucket == "twth-terraform-state-123456789012"
    error_message = "The state bucket must be deterministic per AWS account."
  }

  assert {
    condition     = aws_s3_bucket.terraform_state.force_destroy == false
    error_message = "The state bucket must resist accidental deletion."
  }

  assert {
    condition     = aws_s3_bucket_versioning.terraform_state.versioning_configuration[0].status == "Enabled"
    error_message = "The state bucket must enable versioning."
  }

  assert {
    condition     = one(aws_s3_bucket_server_side_encryption_configuration.terraform_state.rule).apply_server_side_encryption_by_default[0].sse_algorithm == "AES256"
    error_message = "The state bucket must use SSE-S3 encryption."
  }

  assert {
    condition = (
      aws_s3_bucket_public_access_block.terraform_state.block_public_acls &&
      aws_s3_bucket_public_access_block.terraform_state.block_public_policy &&
      aws_s3_bucket_public_access_block.terraform_state.ignore_public_acls &&
      aws_s3_bucket_public_access_block.terraform_state.restrict_public_buckets
    )
    error_message = "Every S3 public-access control must be enabled."
  }

  assert {
    condition     = aws_s3_bucket_lifecycle_configuration.terraform_state.rule[0].noncurrent_version_expiration[0].noncurrent_days == 90
    error_message = "Old state versions must expire after 90 days."
  }

  assert {
    condition     = strcontains(aws_s3_bucket_policy.terraform_state.policy, "aws:SecureTransport")
    error_message = "The state bucket policy must deny non-TLS access."
  }

  assert {
    condition     = aws_route53_zone.service.name == "rpc.example.com"
    error_message = "Bootstrap must own only the delegated service subdomain."
  }

  assert {
    condition = (
      aws_iam_openid_connect_provider.github.url == "https://token.actions.githubusercontent.com" &&
      toset(aws_iam_openid_connect_provider.github.client_id_list) == toset(["sts.amazonaws.com"]) &&
      length(aws_iam_openid_connect_provider.github.thumbprint_list) == 0
    )
    error_message = "GitHub OIDC must use AWS trusted roots and the STS audience."
  }

  assert {
    condition = (
      strcontains(aws_iam_role.github_deploy.assume_role_policy, "repo:example-org@11111111/example-repository@22222222:ref:refs/heads/main") &&
      strcontains(aws_iam_role.github_deploy.assume_role_policy, "sts.amazonaws.com")
    )
    error_message = "The deploy-role trust must use immutable repository IDs and main."
  }

  assert {
    condition = (
      strcontains(aws_iam_role_policy.github_deploy.policy, "twth/application.tfstate") &&
      strcontains(aws_iam_role_policy.github_deploy.policy, "iam:PassRole") &&
      alltrue([
        for action in [
          "route53:ChangeResourceRecordSets",
          "route53:GetHostedZone",
          "route53:ListResourceRecordSets",
          "route53:ListTagsForResource",
          ] : contains(toset(one([
            for statement in jsondecode(aws_iam_role_policy.github_deploy.policy).Statement : statement.Action
            if statement.Sid == "ManageServiceDNS"
        ])), action)
      ]) &&
      one([
        for statement in jsondecode(aws_iam_role_policy.github_deploy.policy).Statement : statement.Resource
        if statement.Sid == "ManageServiceDNS"
      ]) == "arn:aws:route53:::hostedzone/Z0123456789" &&
      alltrue([
        for action in [
          "route53:GetChange",
          "route53:ListHostedZones",
          "route53:ListHostedZonesByName",
          ] : contains(toset(one([
            for statement in jsondecode(aws_iam_role_policy.github_deploy.policy).Statement : statement.Action
            if statement.Sid == "ReadRoute53Changes"
        ])), action)
      ]) &&
      one([
        for statement in jsondecode(aws_iam_role_policy.github_deploy.policy).Statement : statement.Resource
        if statement.Sid == "ReadRoute53Changes"
      ]) == "*" &&
      !strcontains(aws_iam_role_policy.github_deploy.policy, "ecr:") &&
      !strcontains(aws_iam_role_policy.github_deploy.policy, "iam:AttachRolePolicy") &&
      !strcontains(aws_iam_role_policy.github_deploy.policy, "iam:DetachRolePolicy")
    )
    error_message = "The deploy policy must cover the application without ECR or managed-policy attachment permissions."
  }

  assert {
    condition = (
      alltrue([
        for action in [
          "iam:DeleteRolePolicy",
          "iam:GetRolePolicy",
          "iam:PutRolePolicy",
          ] : contains(toset(one([
            for statement in jsondecode(aws_iam_role_policy.github_deploy.policy).Statement : statement.Action
            if statement.Sid == "ManageTaskExecutionRole"
        ])), action)
      ]) &&
      one([
        for statement in jsondecode(aws_iam_role_policy.github_deploy.policy).Statement : statement.Action
        if statement.Sid == "PassTaskExecutionRole"
      ]) == "iam:PassRole" &&
      one([
        for statement in jsondecode(aws_iam_role_policy.github_deploy.policy).Statement : statement.Resource
        if statement.Sid == "PassTaskExecutionRole"
      ]) == "arn:aws:iam::123456789012:role/polygon-rpc-proxy-execution" &&
      one([
        for statement in jsondecode(aws_iam_role_policy.github_deploy.policy).Statement : statement.Condition.StringEquals["iam:PassedToService"]
        if statement.Sid == "PassTaskExecutionRole"
      ]) == "ecs-tasks.amazonaws.com" &&
      !strcontains(aws_iam_role_policy.github_deploy.policy, "iam:*")
    )
    error_message = "The deploy role may manage only the task role's inline policy and pass that role to ECS tasks."
  }

  assert {
    condition = alltrue([
      for action in [
        "elasticloadbalancing:ModifyListener",
        "elasticloadbalancing:SetSecurityGroups",
        "elasticloadbalancing:SetSubnets",
        ] : contains(one([
          for statement in jsondecode(aws_iam_role_policy.github_deploy.policy).Statement : statement.Action
          if statement.Sid == "ManageApplicationInfrastructure"
      ]), action)
    ])
    error_message = "The deploy role must be able to reconcile normal ALB listener, security-group, and subnet changes."
  }

  assert {
    condition = alltrue([
      for action in [
        "ec2:AllocateAddress",
        "ec2:CreateNatGateway",
        "ec2:DeleteNatGateway",
        "ec2:ReleaseAddress",
        ] : contains(toset(one([
          for statement in jsondecode(aws_iam_role_policy.github_deploy.policy).Statement : statement.Action
          if statement.Sid == "ManageApplicationInfrastructure"
      ])), action)
    ])
    error_message = "The deploy role must manage the complete Elastic IP and NAT Gateway lifecycle."
  }

  assert {
    condition = (
      output.aws_account_id == "123456789012" &&
      output.state_bucket_name == "twth-terraform-state-123456789012" &&
      output.hosted_zone_id == "Z0123456789" &&
      output.github_deploy_role_arn == "arn:aws:iam::123456789012:role/twth-github-deploy" &&
      length(output.route53_name_servers) == 4
    )
    error_message = "Bootstrap outputs must provide every operator handoff value."
  }
}
