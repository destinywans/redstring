FROM node:26-alpine AS web-build
WORKDIR /web
RUN npm install -g pnpm@12.4.1
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY shared/ /shared/
COPY web/ ./
RUN VITE_API_MODE=http pnpm build

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
COPY --from=web-build /web/dist /app/web
ENV AIOPS_WEB_DIR=/app/web
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/aiops"]
