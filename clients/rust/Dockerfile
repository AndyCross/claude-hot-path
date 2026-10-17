FROM rust:1-slim-trixie AS build
WORKDIR /src
COPY Cargo.toml client.rs ./
RUN cargo build --release --locked 2>/dev/null || cargo build --release \
 && rustc --version > /toolchain.txt \
 && echo "crates: $(cargo tree --edges normal --prefix none | sort -u | wc -l | tr -d " ")" >> /toolchain.txt

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
COPY --from=build /src/target/release/client /app/client
COPY --from=build /toolchain.txt /toolchain.txt
ENTRYPOINT ["/app/client"]
