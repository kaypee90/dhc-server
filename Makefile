GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
BINARY_NAME=dhc-server
BINARY_UNIX=$(BINARY_NAME)_unix
OUTPUT_HTML_NAME=Preview.html

run:
	go run main.go logging.go handlers.go
build: 
	$(GOBUILD) -o $(BINARY_NAME) -v
test: 
	$(GOTEST) -v ./...
clean: 
	$(GOCLEAN)
	rm -f $(OUTPUT_HTML_NAME)
	rm -f $(BINARY_NAME)
	rm -f $(BINARY_UNIX)
build-run:
	$(GOBUILD) -o $(BINARY_NAME) -v ./...
	./$(BINARY_NAME)
all: test build run
deps:
	$(GOGET) github.com/gin-gonic/gin
	$(GOGET) github.com/stretchr/testify/assert

# Cross compilation
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) -o $(BINARY_UNIX) -v