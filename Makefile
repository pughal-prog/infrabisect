VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: build test vet lint demo demo-kind clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/infrabisect ./cmd/infrabisect

test:
	go test -count=1 ./...

vet:
	go vet ./...

lint:
	golangci-lint run

demo: build
	cd examples/demo && sh setup.sh && ../../bin/infrabisect run --report incident.md

demo-kind: build
	docker pull nginx:1.27-alpine && docker pull nginx:1.27.3-alpine
	cd examples/demo && sh setup.sh && ../../bin/infrabisect run -c infrabisect.kind.yaml --report incident.md

clean:
	rm -rf bin examples/demo/.demo examples/demo/incident.md
