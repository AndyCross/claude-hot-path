FROM golang:1 AS build
WORKDIR /src
COPY client.go ./
RUN go mod init client && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /client client.go \
 && go version > /toolchain.txt

FROM scratch
COPY --from=build /client /app/client
COPY --from=build /toolchain.txt /toolchain.txt
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /usr/local/go/lib/time/zoneinfo.zip
ENTRYPOINT ["/app/client"]
