# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
ENV PATH="/usr/local/go/bin:/go/bin:${PATH}"
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/memdock-server ./cmd/server

FROM alpine:3.21
RUN addgroup -S -g 10001 memdock && adduser -S -D -H -u 10001 -G memdock memdock && mkdir /data && chown memdock:memdock /data
COPY --from=build /out/memdock-server /usr/local/bin/memdock-server
ENV MEMDOCK_ADDR=:8080 MEMDOCK_DB_PATH=/data/memdock.db MEMDOCK_MODE=private
VOLUME ["/data"]
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD ["memdock-server", "healthcheck"]
ENTRYPOINT ["memdock-server"]
