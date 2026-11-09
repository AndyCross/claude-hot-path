FROM mcr.microsoft.com/dotnet/sdk:10.0-alpine AS build
ARG TARGETARCH
RUN apk add --no-cache clang build-base cmake zlib-dev zlib-static openssl-dev openssl-libs-static tzdata
WORKDIR /src
COPY client.csproj Program.cs ./
RUN RID=linux-musl-$( [ "$TARGETARCH" = "amd64" ] && echo x64 || echo arm64 ) \
 && dotnet publish -c Release -r $RID -o /out --nologo \
 && dotnet --version | sed 's/^/dotnet SDK /' > /toolchain.txt

FROM scratch
COPY --from=build /out/client /app/client
COPY --from=build /toolchain.txt /toolchain.txt
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
ENTRYPOINT ["/app/client"]
