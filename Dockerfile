# Stage 1: Build the application
FROM golang:1.26.2-alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum first to leverage Docker layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build a statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o discovery-server .

# Stage 2: Create the minimal runtime image
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /root/
COPY --from=builder /app/discovery-server .

EXPOSE 3001
ENTRYPOINT ["./discovery-server"]