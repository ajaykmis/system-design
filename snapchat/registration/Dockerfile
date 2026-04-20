FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /registration .

FROM alpine:3.20
COPY --from=builder /registration /registration
EXPOSE 8081
CMD ["/registration"]
