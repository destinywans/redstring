FROM node:26-alpine AS web-build
WORKDIR /web
RUN npm install -g pnpm@12.4.1
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY shared/ /shared/
COPY web/ ./
ARG VITE_ENABLE_LEGACY_DELIVERY=false
RUN VITE_API_MODE=http VITE_ENABLE_LEGACY_DELIVERY="$VITE_ENABLE_LEGACY_DELIVERY" pnpm build

FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -o /out/aiops ./cmd/aiops

FROM golang:1.27.3-alpine AS demo-relay-build
WORKDIR /src
COPY ops/demo-compose/relay/main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/demo-relay main.go

FROM alpine:3.23 AS demo-relay
COPY --from=demo-relay-build /out/demo-relay /usr/local/bin/demo-relay
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/demo-relay"]

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates git openssh-client tzdata
ARG REDSTRING_UID=10001
ARG REDSTRING_GID=10001
RUN group="$(getent group "$REDSTRING_GID" | cut -d: -f1)" && \
    if [ -z "$group" ]; then group=aiops; addgroup -g "$REDSTRING_GID" "$group"; fi && \
    adduser -D -u "$REDSTRING_UID" -G "$group" aiops
ARG REDSTRING_REVISION=unknown
ARG REDSTRING_TREE_STATE=unknown
LABEL org.opencontainers.image.revision="$REDSTRING_REVISION" io.redstring.tree-state="$REDSTRING_TREE_STATE"
USER aiops
COPY --from=build /out/aiops /usr/local/bin/aiops
COPY --from=web-build /web/dist /app/web
ENV AIOPS_WEB_DIR=/app/web
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/aiops"]
