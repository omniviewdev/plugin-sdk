.PHONY: proto

proto:
	buf generate

lint:
	golangci-lint run --fix
