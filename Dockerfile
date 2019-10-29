#build stage
FROM golang:1.11.1-alpine AS builder
RUN apk --update --repository http://mirrors.ustc.edu.cn/alpine/v3.8/main/ \
    add \
    git \
    tzdata \
    gcc \
    g++

ENV CGO_ENABLED=1
ENV GOOS=linux

RUN go get -u github.com/gpmgo/gopm

RUN gopm get -g -v github.com/prometheus/client_golang/prometheus && \
    gopm get -g -v github.com/sirupsen/logrus && \
    gopm get -g -v github.com/golang/protobuf && \
    gopm get -g -v github.com/bigwhite/gocmpp && \
    gopm get -g -v github.com/matttproud/golang_protobuf_extensions && \
    gopm get -g -v golang.org/x/sys/unix

WORKDIR /go/src/github.com/sinoreps/cmppproxy
COPY . /go/src/github.com/sinoreps/cmppproxy
RUN gopm get -v -g
RUN go install -ldflags '-s -w'

#final stage
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /go/bin/cmppproxy /cmppproxy
EXPOSE 8080
CMD ["./cmppproxy"]
