FROM golang:1.26-alpine AS build
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY internal/ ./internal/
# Resolve tsnet + yaml.v3 (and transitive deps) and produce go.sum on the
# fly so the repo doesn't have to commit a multi-megabyte go.sum.
RUN go get tailscale.com/tsnet@latest && \
    go get gopkg.in/yaml.v3 && \
    go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/host-agent .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && \
    mkdir -p /var/lib/club-host
COPY --from=build /out/host-agent /usr/local/bin/host-agent
VOLUME ["/var/lib/club-host"]
ENV CLUB_STATE_DIR=/var/lib/club-host \
    CLUB_HOSTNAME=club-host
ENTRYPOINT ["/usr/local/bin/host-agent"]
