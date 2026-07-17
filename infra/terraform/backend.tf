terraform {
  backend "s3" {
    key          = "twth/application.tfstate"
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = true
  }
}
