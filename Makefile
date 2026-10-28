VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/cecobask/imdb-wetrakr-sync/cmd.Version=$(VERSION)
IMAGE ?= iws:dev

.PHONY: build test test-browser lint auth sync package sync-container clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o build/iws .

test:
	go test -race ./...

# Runs the chromedp tests too; needs Chrome or Chromium installed.
test-browser:
	go test -race -tags browser ./...

lint:
	golangci-lint run ./...

auth: build
	./build/iws auth

sync: build
	./build/iws sync

package:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

# Runs the image with the settings from .env and the token kept in ./data.
sync-container: package
	mkdir -p data
	docker run --rm --env-file .env -v "$(CURDIR)/data:/data" $(IMAGE) sync

clean:
	rm -rf build
