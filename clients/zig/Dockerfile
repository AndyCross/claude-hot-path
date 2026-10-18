FROM debian:trixie-slim AS build
ARG TARGETARCH
ARG ZIG_VERSION=0.17.0
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates xz-utils && rm -rf /var/lib/apt/lists/*
RUN ARCH=$( [ "$TARGETARCH" = "amd64" ] && echo x86_64 || echo aarch64 ) \
 && case "$ARCH" in \
      x86_64)  SHA=1cbe9df9f27e6b78d14ccbca43b6703a404ef79ef1c463de901d7f088d4e2026 ;; \
      aarch64) SHA=9e8d11661d4ae3bd57702a3832781e23ad151dde5798e16a5ccd503f65234ff8 ;; \
    esac \
 && curl -sSfL -o /tmp/zig.tar.xz "https://ziglang.org/download/${ZIG_VERSION}/zig-${ARCH}-linux-${ZIG_VERSION}.tar.xz" \
 && echo "${SHA}  /tmp/zig.tar.xz" | sha256sum -c - \
 && mkdir -p /opt/zig && tar -xJf /tmp/zig.tar.xz -C /opt/zig --strip-components=1
WORKDIR /src
COPY client.zig ./
RUN /opt/zig/zig build-exe client.zig -OReleaseSmall -fstrip -femit-bin=/client \
 && echo "zig $(/opt/zig/zig version)" > /toolchain.txt

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
COPY --from=build /client /app/client
COPY --from=build /toolchain.txt /toolchain.txt
ENTRYPOINT ["/app/client"]
