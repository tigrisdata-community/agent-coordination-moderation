group "default" {
  targets = ["ingest", "router"]
}

target "ingest" {
  context    = "."
  dockerfile = "docker/Dockerfile.ingest"
  tags       = ["ghcr.io/tigrisdata/moderation-ingest:latest"]
}

target "router" {
  context    = "."
  dockerfile = "docker/Dockerfile.router"
  tags       = ["ghcr.io/tigrisdata/moderation-router:latest"]
}
