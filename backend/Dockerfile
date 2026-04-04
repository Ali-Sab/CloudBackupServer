# ---- Build stage ----
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy module definition first; go mod tidy will fetch deps and generate go.sum
COPY go.mod go.sum* ./
# If go.sum is missing (first-time setup), tidy will create it
RUN go mod download 2>/dev/null || go mod tidy

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /app/server \
    ./cmd/server

# ---- Runtime stage ----
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/server .

EXPOSE 8080

CMD ["./server"]
