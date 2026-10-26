ARG GO_IMAGE=golang:1.25-alpine3.22
ARG ALPINE_IMAGE=alpine:3.22
ARG REDIS_IMAGE=redis:7.4-alpine
FROM ${REDIS_IMAGE} AS redis

FROM ${GO_IMAGE} AS build
WORKDIR /src/sdn-agent
COPY sdn-agent/go.mod sdn-agent/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY sdn-agent/api ./api
COPY sdn-agent/internal ./internal
COPY sdn-agent/cmd ./cmd
COPY sdn-agent/mcp ./mcp
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/sdn-agent ./cmd/sdn-agent && \
    CGO_ENABLED=0 go build -trimpath -o /out/sdnctl ./cmd/sdnctl && \
    CGO_ENABLED=0 go build -trimpath -o /out/sdn-agent-mcp ./mcp/cmd/sdn-agent-mcp

FROM ${ALPINE_IMAGE}
RUN apk add --no-cache ca-certificates tini bash python3 libssl3 libcrypto3 frr frr-pythontools \
      iproute2 iputils tcpdump procps util-linux && \
    addgroup -S redis && adduser -S -D -H -G redis redis && \
    mkdir -p /usr/share/sdn-agent/frr /var/lib/sdn-agent /var/lib/redis /run/frr /etc/sdn-agent && \
    cp /etc/frr/daemons /usr/share/sdn-agent/frr/daemons && \
    sed -i 's/^bgpd=no/bgpd=yes/;s/^ospfd=no/ospfd=yes/;s/^ospf6d=no/ospf6d=yes/' /usr/share/sdn-agent/frr/daemons
COPY --from=build /out/ /usr/local/bin/
COPY --from=redis /usr/local/bin/redis-server /usr/local/bin/redis-cli /usr/local/bin/
COPY sdn-agent/docker/frr.conf sdn-agent/docker/vtysh.conf /usr/share/sdn-agent/frr/
COPY sdn-agent/docker/entrypoint.sh /usr/local/bin/sdn-agent-entrypoint
COPY sdn-agent/docker/healthcheck.sh /usr/local/bin/sdn-agent-healthcheck
COPY sdn-agent/docker/supervisor.py /usr/local/lib/sdn-agent/supervisor.py
COPY sdn-agent/docker/agent.conf /etc/sdn-agent/agent.conf
RUN chmod 755 /usr/local/bin/sdn-agent-entrypoint /usr/local/bin/sdn-agent-healthcheck && \
    chmod 644 /etc/sdn-agent/agent.conf && \
    cp /usr/share/sdn-agent/frr/* /etc/frr/ && chown -R frr:frr /etc/frr && \
    redis-server --version && redis-cli --version
EXPOSE 9090
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=10s --timeout=10s --start-period=60s --retries=3 \
  CMD /usr/local/bin/sdn-agent-healthcheck
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/sdn-agent-entrypoint"]
CMD ["-config", "/etc/sdn-agent/agent.conf"]
