FROM node:22-bookworm-slim AS node-build
WORKDIR /build
COPY package.json package-lock.json ./
COPY runtime/package.json runtime/package.json
COPY web/package.json web/package.json
COPY packages/plugin-sdk/package.json packages/plugin-sdk/package.json
COPY plugins/example/package.json plugins/example/package.json
COPY plugins/mail/package.json plugins/mail/package.json
COPY plugins/video/package.json plugins/video/package.json
RUN npm ci
COPY runtime runtime
COPY web web
COPY packages packages
COPY plugins plugins
RUN npm run build

FROM golang:1.26-bookworm AS go-build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 go build -trimpath -o /secretary ./cmd/secretary

FROM node:22-bookworm-slim AS backend
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=go-build /secretary /app/secretary
COPY --from=node-build /build/plugins /app/plugins
RUN mkdir -p /app/data && chown -R node:node /app
USER node
ENV DATA_DIR=/app/data PLUGIN_DIR=/app/plugins LISTEN_ADDR=:8080
EXPOSE 8080
CMD ["/app/secretary"]

FROM node:22-bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git ripgrep && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=node-build /build/node_modules /app/node_modules
COPY --from=node-build /build/runtime /app/runtime
RUN mkdir -p /app/data && chown -R node:node /app/data
USER node
ENV RUNTIME_HOST=0.0.0.0 RUNTIME_DATA_DIR=/app/data
EXPOSE 8091
CMD ["node","runtime/dist/server.js"]

FROM nginx:1.28-alpine AS web
COPY --from=node-build /build/web/dist /usr/share/nginx/html
COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
