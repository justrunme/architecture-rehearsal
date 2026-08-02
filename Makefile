BIN ?= bin/rehearsal
GO ?= go

.PHONY: all build test demo demo-rwo demo-cni demo-prom tidy clean

all: test build

build:
	$(GO) build -o $(BIN) ./cmd/rehearsal

test:
	$(GO) test ./...

tidy:
	$(GO) mod tidy

demo: build demo-rwo demo-cni demo-prom
	@echo "All golden demos written under ./out"

demo-rwo: build
	-$(BIN) analyze \
	  --baseline examples/golden/rwo-node-loss/baseline.json \
	  --change examples/golden/rwo-node-loss/change.json \
	  --out out \
	  --html out/rwo-report.html \
	  --quiet

demo-cni: build
	-$(BIN) analyze \
	  --baseline examples/golden/cni-ip-capacity/baseline.json \
	  --change examples/golden/cni-ip-capacity/change.json \
	  --out out \
	  --html out/cni-report.html \
	  --quiet

demo-prom: build
	-$(BIN) analyze \
	  --baseline examples/golden/prom-zero-match/baseline.json \
	  --change examples/golden/prom-zero-match/change.json \
	  --out out \
	  --html out/prom-report.html \
	  --quiet

clean:
	rm -rf bin out
