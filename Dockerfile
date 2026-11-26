FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/pandoc-worker ./cmd/pandoc-worker

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      pandoc \
      weasyprint \
      poppler-utils \
      fonts-dejavu-core \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/pandoc-worker /usr/local/bin/pandoc-worker
USER nobody
ENTRYPOINT ["/usr/local/bin/pandoc-worker"]
