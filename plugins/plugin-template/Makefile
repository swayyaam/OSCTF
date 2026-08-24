# OSCTF plugin — build, test, package. You do not need to know anything about the platform
# internals; everything goes through the public github.com/swayyaam/OSCTF/plugin/sdk.

BINARY := my-plugin

.PHONY: build test package tidy

build: ## Compile the plugin binary
	go build -o $(BINARY) .

test: ## Build + run the contract test (proves the plugin satisfies the OSCTF ABI)
	go test ./...

package: build ## Produce the loadable plugin directory: dist/<name>/ with the binary + manifest
	rm -rf dist/$(BINARY)
	mkdir -p dist/$(BINARY)
	cp $(BINARY) plugin.yaml dist/$(BINARY)/
	@echo "packaged: dist/$(BINARY)/ — copy this directory into the host's plugins directory"

tidy: ## Resolve/prune module dependencies
	go mod tidy
