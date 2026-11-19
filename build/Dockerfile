# Docstorio image: Factorio headless server + docstorio manager.
# Build it with build/build.sh, which resolves FACTORIO_VERSION and
# FACTORIO_SHA256 and reads DOCSTORIO_VERSION from VERSION.
# The build context is the repository root.

# --- Stage 1: vet, test and build the manager -------------------------------
FROM golang:1.25.14 AS manager
ARG DOCSTORIO_VERSION=dev
WORKDIR /src
COPY . .
RUN go vet ./... && go test ./...
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${DOCSTORIO_VERSION}" \
      -o /out/docstorio ./cmd/docstorio

# --- Stage 2: download and verify Factorio -----------------------------------
FROM debian:13-slim AS factorio
ARG FACTORIO_VERSION
ARG FACTORIO_SHA256
RUN if [ -z "${FACTORIO_VERSION}" ] || [ -z "${FACTORIO_SHA256}" ]; then \
      echo "FACTORIO_VERSION and FACTORIO_SHA256 build args are required (use build/build.sh)" >&2; \
      exit 1; \
    fi
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl xz-utils \
 && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL -o /tmp/factorio.tar.xz \
      "https://factorio.com/get-download/${FACTORIO_VERSION}/headless/linux64" \
 && echo "${FACTORIO_SHA256}  /tmp/factorio.tar.xz" | sha256sum -c - \
 && tar -xJf /tmp/factorio.tar.xz -C /opt --no-same-owner \
 && rm /tmp/factorio.tar.xz \
 && test -x /opt/factorio/bin/x64/factorio

# --- Stage 3: runtime --------------------------------------------------------
FROM debian:13-slim
ARG FACTORIO_VERSION
ARG DOCSTORIO_VERSION=dev
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tini tzdata \
 && rm -rf /var/lib/apt/lists/*
COPY --from=factorio /opt/factorio /opt/factorio
COPY --from=manager /out/docstorio /usr/local/bin/docstorio
COPY build/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/docstorio /usr/local/bin/entrypoint.sh
ENV FACTORIO_VERSION=${FACTORIO_VERSION} \
    DOCSTORIO_VERSION=${DOCSTORIO_VERSION} \
    PUID=845 \
    PGID=845 \
    TZ=UTC
EXPOSE 8080/tcp 34197/udp
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
