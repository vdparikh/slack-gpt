# Start from the latest golang base image
FROM golang:latest AS builder

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy everything from the current directory to the Working Directory inside the container
COPY . .

# Build the Go app
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd/app/app.go

# Start a new stage from scratch
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy the pre-built binary file and config file from the previous stage
COPY --from=builder /app/main .
COPY --from=builder /app/config.yaml .

# Command to run the executable
CMD ["./main"]