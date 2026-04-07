FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o pocketbase .

FROM alpine:3.21
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/pocketbase .
VOLUME /pb_data
EXPOSE 8090
CMD ["./pocketbase", "serve", "--http=0.0.0.0:8090", "--dir=/pb_data"]
