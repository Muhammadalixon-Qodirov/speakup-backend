FROM golang:1.26.5-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o speakup cmd/server/main.go

FROM alpine:3.20
RUN apk --no-cache add ca-certificates
RUN adduser -D -g '' appuser
WORKDIR /app
COPY --from=builder /app/speakup .
USER appuser
EXPOSE 8082
CMD ["./speakup"]
