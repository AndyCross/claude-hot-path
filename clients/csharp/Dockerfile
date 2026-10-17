FROM mcr.microsoft.com/dotnet/sdk:10.0 AS build
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends clang zlib1g-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY client.csproj Program.cs ./
RUN RID=linux-$( [ "$TARGETARCH" = "amd64" ] && echo x64 || echo arm64 ) \
 && dotnet publish -c Release -r $RID -o /out --nologo \
 && dotnet --version | sed 's/^/dotnet SDK /' > /toolchain.txt

FROM mcr.microsoft.com/dotnet/runtime-deps:10.0
COPY --from=build /out/client /app/client
COPY --from=build /toolchain.txt /toolchain.txt
ENTRYPOINT ["/app/client"]
