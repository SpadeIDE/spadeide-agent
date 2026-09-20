.PHONY: help agent backend serve linux linux-deb linux-server linux-amd linux-arm release test smoke clean

help:
	@echo "Targets:"
	@echo "  make agent         Build host CLI for this machine → dist/agent/"
	@echo "  make serve         Run local agent (spadeide serve)"
	@echo "  make linux         Linux .deb + server tarballs (amd64+arm64)"
	@echo "  make linux-deb     Ubuntu/Debian .deb only"
	@echo "  make linux-server  Headless server tarballs only"
	@echo "  make linux-amd     Linux amd64 only"
	@echo "  make linux-arm     Linux arm64 only"
	@echo "  make release       Build + stage github-release/ (VERSION=0.1.0)"
	@echo "  make test          go test ./..."
	@echo "  make smoke         Print smoke-test instructions"
	@echo "  make clean         Remove dist/ and github-release/"

agent backend:
	./scripts/build-agent.sh

serve: agent
	./dist/agent/spade-agent serve

linux:
	./scripts/build-linux.sh

linux-deb:
	KIND=deb ./scripts/build-linux.sh

linux-server:
	KIND=server ./scripts/build-linux.sh

linux-amd:
	ARCH=amd64 ./scripts/build-linux.sh

linux-arm:
	ARCH=arm64 ./scripts/build-linux.sh

# VERSION=0.2.0 make release
release:
	./scripts/build-release.sh $(VERSION)

test:
	cd agent && go test ./...

smoke:
	@echo "Start: make serve"
	@echo "Then:  cd agent && go run ./cmd/smoke <secret> <host> <port> <fingerprint>"

clean:
	rm -rf dist github-release
