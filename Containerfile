# Build
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/ringqswitch ./cmd/ringqswitch \
 && CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/rq_cli ./cmd/rq_cli

# Run. Host networking is strongly recommended for SIP/RTP, e.g.:
#   <container tool> run --network host -v $PWD/conf:/etc/ringqswitch ringqswitch
FROM alpine:3.20
RUN adduser -D -H -s /sbin/nologin ringq \
 && mkdir -p /etc/ringqswitch /var/lib/ringqswitch/log /var/lib/ringqswitch/recordings /var/lib/ringqswitch/sounds \
 && chown -R ringq /var/lib/ringqswitch
COPY --from=build /out/ringqswitch /out/rq_cli /usr/local/bin/
COPY conf /etc/ringqswitch
# The sample config uses paths relative to the conf dir; point them at the
# writable data directory instead.
RUN sed -i -e 's#"../log/cdr.csv"#"/var/lib/ringqswitch/log/cdr.csv"#' \
           -e 's#"../sounds"#"/var/lib/ringqswitch/sounds"#' \
           -e 's#"../recordings"#"/var/lib/ringqswitch/recordings"#' /etc/ringqswitch/ringqswitch.xml
USER ringq
EXPOSE 5060/udp 5060/tcp 16384-32768/udp
ENTRYPOINT ["/usr/local/bin/ringqswitch", "-conf", "/etc/ringqswitch"]
