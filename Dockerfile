# syntax=docker/dockerfile:1

# ---- build stage -----------------------------------------------------------
FROM golang:1.27.1-alpine AS build
WORKDIR /src

COPY go.mod ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# CGO stays off so the binary is fully static and can run on scratch.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/proxy ./cmd/proxy

# ---- runtime stage ---------------------------------------------------------
FROM scratch
COPY --from=build /out/proxy /proxy
# A default config ships with the image; compose/demo mounts override it.
# Backend URLs must point at reachable hosts for the deployment topology.
COPY config/config.json /etc/grf/config.json

EXPOSE 8080

ENTRYPOINT ["/proxy"]
# Default config path; `docker run ... /proxy -config <other>` overrides it via CMD.
CMD ["-config", "/etc/grf/config.json"]
