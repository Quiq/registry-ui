FROM golang:1.10.3-alpine as builder

ENV GOPATH /opt

RUN apk update && \
    apk add ca-certificates git build-base && \
    go get github.com/Masterminds/glide

ADD glide.* /opt/src/github.com/quiq/docker-registry-ui/
RUN cd /opt/src/github.com/quiq/docker-registry-ui && \
    /opt/bin/glide install

ADD events /opt/src/github.com/quiq/docker-registry-ui/events
ADD registry /opt/src/github.com/quiq/docker-registry-ui/registry
ADD *.go /opt/src/github.com/quiq/docker-registry-ui/
RUN cd /opt/src/github.com/quiq/docker-registry-ui && \
    go test -v ./registry && \
    go build -o /opt/docker-registry-ui github.com/quiq/docker-registry-ui


FROM alpine:3.7

WORKDIR /opt
RUN apk add --no-cache ca-certificates && \
    mkdir /opt/data

ADD templates /opt/templates
ADD static /opt/static
COPY --from=builder /opt/docker-registry-ui /opt/

USER nobody
ENTRYPOINT ["/opt/docker-registry-ui"]
