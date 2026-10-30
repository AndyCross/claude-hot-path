FROM debian:trixie-slim AS build
ARG CURL_VERSION=8.23.0
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev make curl ca-certificates xz-utils libmbedtls-dev && rm -rf /var/lib/apt/lists/*
RUN curl -sSfL -o /tmp/curl.tar.xz "https://curl.se/download/curl-${CURL_VERSION}.tar.xz" \
 && mkdir -p /tmp/curl && tar -xJf /tmp/curl.tar.xz -C /tmp/curl --strip-components=1 \
 && cd /tmp/curl \
 && ./configure --disable-shared --enable-static --with-mbedtls --with-ca-bundle=/etc/ssl/certs/ca-certificates.crt --without-libpsl \
      --disable-ftp --disable-file --disable-ldap --disable-ldaps --disable-rtsp --disable-dict \
      --disable-telnet --disable-tftp --disable-pop3 --disable-imap --disable-smb --disable-smtp \
      --disable-gopher --disable-mqtt --disable-manual --disable-libcurl-option --disable-docs \
      --disable-proxy --disable-cookies --disable-doh --disable-netrc --disable-alt-svc --disable-hsts \
      --disable-websockets --disable-ntlm --disable-unix-sockets --disable-progress-meter \
      --without-zlib --without-brotli --without-zstd --without-libidn2 --without-nghttp2 \
      --prefix=/opt/curl >/tmp/configure.log \
 && make -j"$(nproc)" -C lib >/tmp/make.log && make -C lib install >/dev/null && make -C include install >/dev/null
WORKDIR /src
COPY client.c ./
RUN gcc -std=c11 -Os -s -ffunction-sections -fdata-sections -Wl,--gc-sections -o /client client.c \
      -I/opt/curl/include -DCURL_STATICLIB /opt/curl/lib/libcurl.a -lmbedtls -lmbedx509 -lmbedcrypto -static \
 && { gcc --version | head -1; echo "libcurl ${CURL_VERSION} static, HTTP only, mbedTLS $(dpkg-query -W -f='${Version}' libmbedtls-dev)"; } > /toolchain.txt

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
COPY --from=build /client /app/client
COPY --from=build /toolchain.txt /toolchain.txt
ENTRYPOINT ["/app/client"]
