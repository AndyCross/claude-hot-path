FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends bash curl ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && { bash --version | head -1; curl --version | head -1; } > /toolchain.txt
ENV LC_ALL=C.UTF-8
COPY client.sh /app/client
ENTRYPOINT ["/app/client"]
