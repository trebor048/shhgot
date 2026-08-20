FROM golang:1.21-alpine AS builder
WORKDIR /build
COPY . .
RUN go build -o shhgit-server main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /build/shhgit-server .
EXPOSE 8000
CMD ["./shhgit-server", "-port", "8000"]
