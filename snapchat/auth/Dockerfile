FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /auth .

FROM alpine:3.20
COPY --from=builder /auth /auth
EXPOSE 8082
CMD ["/auth"]
