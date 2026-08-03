BIN ?= bin/rehearsal
GO ?= go

.PHONY: all build test race vet demo e2e tidy clean docker image verify-example

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

docker image:
	docker build -t ghcr.io/justrunme/architecture-rehearsal:1.2.0 -f Dockerfile .

release-assets:
	bash scripts/release-assets.sh 1.2.0

verify-example: build
	$(BIN) analyze \
	  --baseline examples/golden/rwo-node-loss/baseline.json \
	  --change examples/golden/rwo-node-loss/change.json \
	  --out out --quiet || true
	@# post-deploy observed fixture claims the predicted failure happened
	$(BIN) verify \
	  --report out/latest-report.json \
	  --observed examples/golden/rwo-node-loss/observed.json \
	  --out out/verify.json

clean:
	rm -rf bin out
