default: fmt lint install generate

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run

generate:
	cd tools; go generate -tags generate ./...

fmt:
	gofmt -s -w -e .

# All tests run against an in-memory mock of the ConnByte API. They need a terraform or tofu
# binary in PATH (or TF_ACC_TERRAFORM_PATH) but no ConnByte account.
test:
	go test -v -cover -timeout=300s ./...

.PHONY: fmt lint test build install generate
