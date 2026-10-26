FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
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

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk --no-cache upgrade && apk add --no-cache ca-certificates \
    && addgroup -g 10001 app && adduser -D -u 10001 -G app app
COPY --from=build /analytics-service /usr/local/bin/analytics-service
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/analytics-service"]
