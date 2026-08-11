FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -buildvcs=false -o /out/catpaw2api ./cmd/server && \
    go build -buildvcs=false -o /out/catpaw2api-login ./cmd/login && \
    go build -buildvcs=false -o /out/catpaw2api-credit ./cmd/credit && \
    go build -buildvcs=false -o /out/catpaw2api-apply ./cmd/apply

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/ /usr/local/bin/
COPY config.example.json ./config.example.json
VOLUME ["/app/auths", "/app/data"]
EXPOSE 7867
ENV CP2A_API_KEY=changeme
CMD ["catpaw2api", "-config", "config.json"]
