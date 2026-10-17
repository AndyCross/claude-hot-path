FROM golang:1 AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /mock .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates wget
COPY --from=build /mock /mock
EXPOSE 8080
ENTRYPOINT ["/mock"]
