FROM golang:1.27-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -o /out/coordinator ./cmd/coordinator \
 && CGO_ENABLED=0 go build -buildvcs=false -trimpath -o /out/storage-node ./cmd/storage-node \
 && CGO_ENABLED=0 go build -buildvcs=false -trimpath -o /out/migrate ./cmd/migrate
FROM alpine:3.22
RUN apk add --no-cache ca-certificates wget && addgroup -S app && adduser -S -G app app && mkdir -p /data && chown app:app /data
WORKDIR /app
COPY --from=build /out/ /app/
USER app
EXPOSE 8080 8081 8082
CMD ["/app/coordinator"]
