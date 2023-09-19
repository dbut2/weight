FROM golang:alpine AS builder

WORKDIR /app

COPY ./go.mod ./go.mod
COPY ./go.sum ./go.sum

COPY ./weight.html ./weight.html
COPY ./main.go ./main.go

RUN go build -o /server .

FROM alpine

WORKDIR /app

COPY --from=builder /server ./server

CMD ["./server"]
