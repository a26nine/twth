locals {
  container_name        = "rpc-proxy"
  container_port        = 8080
  ghcr_image_repository = "ghcr.io/a26nine/twth-rpc-proxy"
  image_uri             = "${local.ghcr_image_repository}@${var.image_digest}"
}
