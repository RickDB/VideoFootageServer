.PHONY: run test build docker-build compose-local
run:
	go run ./cmd/server
test:
	go test ./...
build:
	go build -buildvcs=false -o nvr-evidence ./cmd/server
docker-build:
	docker build -t hikfootageserver:local .
compose-local:
	docker compose -f docker-compose.yml -f docker-compose.build.yml up --build
