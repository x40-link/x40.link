FROM docker.io/library/golang:1.26 AS build
ARG GOARCH="amd64"

ENV TASK_VERSION="v3.33.1"
ENV GOOS="linux" GOARCH="${GOARCH}"

# Install the task runner
RUN curl \
        --output /tmp/task.tar.gz \
        --location \
        "https://github.com/go-task/task/releases/download/$TASK_VERSION/task_linux_amd64.tar.gz" \
    && tar \
        --directory /tmp \
        --extract \
        --file /tmp/task.tar.gz \
    && chmod +x /tmp/task \
    && mv /tmp/task /usr/bin/task && \
    rm -rf /tmp/*

# Copy in the source files
WORKDIR /mnt
COPY . /mnt

# Build the binary
RUN task bin

# An imagine with SSL certificates (and some other Linux niceties)
# See
# * https://github.com/GoogleContainerTools/distroless/blob/main/base/README.md
# * https://console.cloud.google.com/gcr/images/distroless/GLOBAL/static
#
# Ignore the image pinning requirement — distroless is essentialy empty.
# hadolint ignore=DL3007
FROM gcr.io/distroless/static:latest AS run
ARG GOARCH="amd64"

# The standard OCI Labels. See:
# https://github.com/opencontainers/image-spec/blob/main/annotations.md
ARG VERSION="dev"
ARG CREATED="1970-01-01"

LABEL "org.opencontainers.image.title"="x40.link" \
    org.opencontainers.image.description="The server for the @.link shortening service" \
    org.opencontainers.image.created="${CREATED}" \
    org.opencontainers.image.authors="Andrew Howden <hello@andrewhowden.com>" \
    org.opencontainers.image.url="https://www.x40.dev/" \
    org.opencontainers.image.documentation="https://www.x40.dev/" \
    org.opencontainers.image.source="https://github.com/andrewhowdencom/x40.link/tree/main/Containerfile" \
    org.opencontainers.image.version="${VERSION}" \
    org.opencontainers.image.revision="${VERSION}" \
    org.opencontainers.image.vendor="Andrew Howden <hello@andrewhowden.com>" \
    org.opencontainers.image.licenses="AGPL"



ENV GOOS="linux"

COPY --from=build /mnt/dist/linux+${GOARCH}/x40.link /usr/bin/x40.link

CMD ["/usr/bin/x40.link", "serve", "--storage.boltdb.file", "/tmp/urls.db", "--server.listen-address", "0.0.0.0:80"]
