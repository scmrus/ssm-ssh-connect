.PHONY: build install clean version

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o ssm-ssh-connect

install:
	go install -ldflags "$(LDFLAGS)"

clean:
	rm -f ssm-ssh-connect

version:
	@echo $(VERSION)

# Cross-compile for macOS
build-darwin-amd64:
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o ssm-ssh-connect-darwin-amd64

build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o ssm-ssh-connect-darwin-arm64

build-all: build-darwin-amd64 build-darwin-arm64
