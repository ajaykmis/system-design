FROM golang:1.24-alpine AS builder
ENV GOTOOLCHAIN=auto
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /server ./websockets

FROM alpine:3.20
COPY --from=builder /server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
