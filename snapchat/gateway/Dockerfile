FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /gateway .

FROM alpine:3.20
COPY --from=builder /gateway /gateway
EXPOSE 8080
CMD ["/gateway"]
