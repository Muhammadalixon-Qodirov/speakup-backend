# SpeakUp Backend - Makefile

# Start PostgreSQL + Redis via Docker
db-up:
	docker-compose up -d

# Stop databases
db-down:
	docker-compose down

# Run server in development mode
run:
	go run cmd/server/main.go

# Build production binary
build:
	go build -o bin/speakup cmd/server/main.go

# Run the built binary
start:
	./bin/speakup

# Tidy dependencies
tidy:
	go mod tidy

# Run tests
test:
	go test ./... -v

.PHONY: db-up db-down run build start tidy test
