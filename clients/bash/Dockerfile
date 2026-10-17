FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends bash curl ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/*
ENV LC_ALL=C.UTF-8
COPY client.sh /app/client
ENTRYPOINT ["/app/client"]
