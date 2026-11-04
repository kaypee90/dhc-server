GOCMD=go
BINARY_NAME=dhc-server
BINARY_UNIX=$(BINARY_NAME)_unix
VERSION ?= dev
LDFLAGS=-ldflags "-s -w -X main.Version=$(VERSION)"

.PHONY: run build test lint vuln clean build-run all deps build-linux docker

# Local run with authentication disabled. Never do this in production.
run:
	DHC_AUTH_DISABLED=true $(GOCMD) run .
build:
	CGO_ENABLED=0 $(GOCMD) build -trimpath $(LDFLAGS) -o $(BINARY_NAME) .
test:
	$(GOCMD) test -race -v ./...
lint:
	$(GOCMD) vet ./...
vuln:
	$(GOCMD) run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
clean:
	$(GOCMD) clean
	rm -f $(BINARY_NAME) $(BINARY_UNIX)
build-run: build
	./$(BINARY_NAME)
all: lint test build
deps:
	$(GOCMD) mod download

# Cross compilation. Works without cgo because the SQLite driver is pure Go.
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOCMD) build -trimpath $(LDFLAGS) -o $(BINARY_UNIX) .
docker:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY_NAME):$(VERSION) .
