FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/aiops ./cmd/aiops

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 aiops
USER aiops
COPY --from=build /out/aiops /usr/local/bin/aiops
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/aiops"]
