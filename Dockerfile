# Build stage
FROM golang:1 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o app

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

COPY --from=builder /app/app .

# Short links live in a bbolt file, so the data directory should be a volume.
ENV DB_PATH=/data/podcast-filter.db
VOLUME /data

EXPOSE 8080

CMD ["./app"]
