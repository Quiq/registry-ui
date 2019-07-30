FROM golang:1.12.7-alpine3.9 as builder

ENV GOPATH /opt
ENV GO111MODULE on

RUN apk update && \
    apk add ca-certificates git bash gcc musl-dev

WORKDIR /opt/src/github.com/quiq/docker-registry-ui
ADD events events
ADD registry registry
ADD *.go go.mod go.sum ./

RUN go test -v ./registry && \
    go build -o /opt/docker-registry-ui *.go


FROM alpine:3.9

WORKDIR /opt
RUN apk add --no-cache ca-certificates && \
    mkdir /opt/data

ADD templates /opt/templates
ADD static /opt/static
COPY --from=builder /opt/docker-registry-ui /opt/

USER nobody
ENTRYPOINT ["/opt/docker-registry-ui"]
