.PHONY: fmt test vet race build migrate-up migrate-down
fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')
test:
	go test ./...
race:
	go test -race ./...
vet:
	go vet ./...
build:
	go build -buildvcs=false ./cmd/coordinator ./cmd/storage-node ./cmd/migrate
migrate-up:
	go run ./cmd/migrate up
migrate-down:
	go run ./cmd/migrate down
