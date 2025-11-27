FROM golang:1.25.4-alpine3.22 AS builder

RUN apk update && \
    apk add ca-certificates git bash gcc musl-dev

WORKDIR /opt/src
ADD events events
ADD registry registry
ADD *.go go.mod go.sum ./

RUN go test -v ./registry && \
    go build -o /opt/registry-ui *.go


FROM alpine:3.22

WORKDIR /opt
RUN apk add --no-cache ca-certificates tzdata && \
    mkdir /opt/data && \
    chown nobody /opt/data

ADD templates /opt/templates
ADD static /opt/static
ADD config.yml /opt
COPY --from=builder /opt/registry-ui /opt/

USER nobody
ENTRYPOINT ["/opt/registry-ui"]
