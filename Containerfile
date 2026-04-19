# Stage 1: Build frontend
FROM registry.access.redhat.com/ubi9/nodejs-22:latest AS frontend-builder
USER 0
WORKDIR /app
COPY package.json yarn.lock* ./
RUN npm install -g yarn && yarn install --frozen-lockfile || yarn install
COPY tsconfig.json webpack.config.ts console-extensions.json ./
COPY src/ src/
RUN NODE_ENV=production yarn build

# Stage 2: Build Go backend
FROM registry.redhat.io/ubi10/go-toolset:10.1 AS backend-builder
USER 0
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/
RUN CGO_ENABLED=0 GOOS=linux go build -o /backend ./cmd/backend/

# Stage 3: Final image
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

LABEL org.opencontainers.image.title="RHAI Workshop Plugin"
LABEL org.opencontainers.image.description="OpenShift Console Plugin for RHAI workshops with Go backend"

WORKDIR /app
COPY --from=backend-builder /backend /app/backend
COPY --from=frontend-builder /app/dist /app/dist

ENV PLUGIN_DIST_DIR=/app/dist
ENV PORT=9443

RUN chown -R 1001:0 /app && chmod -R g=u /app

USER 1001
EXPOSE 9443

CMD ["/app/backend"]
