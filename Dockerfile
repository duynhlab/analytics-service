FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY config ./config
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /analytics-service ./cmd

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk --no-cache upgrade && apk add --no-cache ca-certificates \
    && addgroup -g 10001 app && adduser -D -u 10001 -G app app
COPY --from=build /analytics-service /usr/local/bin/analytics-service
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/analytics-service"]
