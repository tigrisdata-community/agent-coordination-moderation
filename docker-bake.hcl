group "default" {
  targets = ["ingest", "router"]
}

target "ingest" {
  context    = "."
  dockerfile = "docker/Dockerfile.ingest"
  tags       = ["ghcr.io/tigrisdata-community/agent-coordination-moderation/ingest:latest"]
  platforms  = [ "linux/amd64" ]
}

target "router" {
  context    = "."
  dockerfile = "docker/Dockerfile.router"
  tags       = ["ghcr.io/tigrisdata-community/agent-coordination-moderation/router:latest"]
  platforms  = [ "linux/amd64" ]
}
