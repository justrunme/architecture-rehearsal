BIN ?= bin/rehearsal
GO ?= go

.PHONY: all build test race vet demo e2e e2e-argo e2e-argo-presync tidy clean docker image verify-example

all: vet test build

build:
	$(GO) build -o $(BIN) ./cmd/rehearsal

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

demo: build
	bash scripts/demo.sh

# v0.4 iron path: real YAML dump → graph → scoped change → analyze → verify
e2e: build
	bash scripts/e2e_pipeline.sh

# Argo CD impact gate (fixture scenarios; Kind variant: scripts/kind-argo-impact-e2e.sh)
e2e-argo: build
	bash scripts/e2e_argo_impact.sh

# Argo CD PreSync hook gate (Kind + pinned Argo CD)
e2e-argo-presync: build
	bash scripts/kind-argo-presync-e2e.sh

docker image:
	docker build -t ghcr.io/justrunme/architecture-rehearsal:1.7.1 -f Dockerfile .
	docker build -t ghcr.io/justrunme/architecture-rehearsal-operator:1.7.1 -f Dockerfile.operator .
	docker build -t ghcr.io/justrunme/architecture-rehearsal-presync:1.7.1 -f Dockerfile.presync .

release-assets:
	bash scripts/release-assets.sh 1.7.1

verify-example: build
	@set +e; \
	  $(BIN) analyze \
	    --baseline examples/golden/rwo-node-loss/baseline.json \
	    --change examples/golden/rwo-node-loss/change.json \
	    --out out --quiet; \
	  code=$$?; \
	  set -e; \
	  test $$code -eq 3
	@# post-deploy observed fixture claims the predicted failure happened
	$(BIN) verify \
	  --report out/latest-report.json \
	  --observed examples/golden/rwo-node-loss/observed.json \
	  --baseline examples/golden/rwo-node-loss/baseline.json \
	  --change examples/golden/rwo-node-loss/change.json \
	  --out out/verify.json
	@test "$$(python3 -c 'import json; print(json.load(open("out/verify.json"))["outcome"])')" = "verified"

clean:
	rm -rf bin out
