# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

# Camada de dependências separada para aproveitar o cache entre builds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/vodmanager ./cmd/vodmanager

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 vodm
COPY --from=build /out/vodmanager /usr/local/bin/vodmanager

USER vodm
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/vodmanager"]
