FROM golang:alpine AS builder

WORKDIR /app

COPY ./go.mod ./go.mod
COPY ./go.sum ./go.sum

COPY ./pages ./pages
COPY ./main.go ./main.go
COPY ./health.go ./health.go

RUN go build -o /server .

FROM alpine

WORKDIR /app

COPY --from=builder /server ./server

CMD ["./server"]
