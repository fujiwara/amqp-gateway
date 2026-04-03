.PHONY: clean test

amqp-gateway: go.* *.go
	go build -o $@ ./cmd/amqp-gateway

clean:
	rm -rf amqp-gateway dist/

test:
	go test -v ./...

install:
	go install github.com/fujiwara/amqp-gateway/cmd/amqp-gateway

dist:
	goreleaser build --snapshot --clean
