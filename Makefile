.PHONY: help agent backend serve linux linux-deb linux-server linux-amd linux-arm mac release test lint clean

help:
	@echo "Targets:"
	@echo "  make agent         Build host CLI for this machine → dist/agent/"
	@echo "  make serve         Run local agent (spadeide serve)"
	@echo "  make linux         Linux .deb + server tarballs (amd64+arm64) → dist/linux/"
	@echo "  make linux-deb     Ubuntu/Debian .deb only"
	@echo "  make linux-server  Headless server tarballs only"
	@echo "  make linux-amd     Linux amd64 only"
	@echo "  make linux-arm     Linux arm64 only"
	@echo "  make mac           arm64 Agent.app DMG, ad-hoc → dist/mac/"
	@echo "  make release       Linux + signed macOS → github-release/ (VERSION=0.1.0)"
	@echo "  make test          go test -race ./..."
	@echo "  make lint          golangci-lint run"
	@echo "  make clean         Remove dist/ and github-release/"

agent backend:
	./scripts/build-agent.sh

serve: agent
	./dist/agent/spade-agent serve

linux:
	SKIP_STAGE=1 ./scripts/build-linux.sh all

linux-deb:
	SKIP_STAGE=1 KIND=deb ./scripts/build-linux.sh all

linux-server:
	SKIP_STAGE=1 KIND=server ./scripts/build-linux.sh all

linux-amd:
	SKIP_STAGE=1 ./scripts/build-linux.sh amd64

linux-arm:
	SKIP_STAGE=1 ./scripts/build-linux.sh arm64

mac:
	./scripts/build-mac.sh

# VERSION=0.2.0 make release
release:
	./scripts/build-release.sh $(VERSION)

test:
	cd agent && go test -race ./...

lint:
	@cd agent && dirty=$$(gofmt -l .); \
	  if [ -n "$$dirty" ]; then echo "gofmt needed:"; echo "$$dirty"; exit 1; fi
	@cd agent && \
	  if command -v goimports >/dev/null 2>&1; then \
	    dirty=$$(goimports -l -local github.com/spadeide/spade-agent .); \
	    if [ -n "$$dirty" ]; then echo "goimports needed:"; echo "$$dirty"; exit 1; fi; \
	  else \
	    echo "goimports not installed — skipping import-order check (brew install goimports / go install golang.org/x/tools/cmd/goimports@latest)"; \
	  fi
	@if command -v golangci-lint >/dev/null 2>&1; then \
	  cd agent && golangci-lint run ./...; \
	else \
	  echo "golangci-lint not installed — install: https://golangci-lint.run/welcome/install/"; \
	  echo "(gofmt/goimports checks above still ran)"; \
	fi
	@echo "lint ok"

clean:
	rm -rf dist github-release
