VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "devel")
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build install clean

build:
	go build -ldflags "$(LDFLAGS)" -o ./dist/claudette .

install:
	go install -ldflags "$(LDFLAGS)" .

clean:
	rm -rf dist