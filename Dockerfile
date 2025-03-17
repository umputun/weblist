FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install necessary dependencies
RUN apk add --no-cache git

# Copy go.mod and go.sum files first and download dependencies
COPY go.mod go.sum* ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o file-server .

# Use a minimal alpine image for the final container
FROM alpine:3.19

WORKDIR /app

# Install any runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Copy the built binary from the builder stage
COPY --from=builder /app/file-server /app/file-server

# No need to copy templates and assets as they are embedded in the binary

# Create a volume for the content to be served
VOLUME ["/data"]

# Set the working directory to the mounted volume
WORKDIR /data

# Expose the port the app will run on
EXPOSE 8080

# Run the application
ENTRYPOINT ["/app/file-server"]
CMD ["--listen=:8080", "--theme=light"]