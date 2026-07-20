# OSCTF production image. Multi-stage: build the SPA, embed it, build one static binary.
# Spec: docs/v0.1/10-deployment.md

# --- stage web: build the dashboard ------------------------------------------
FROM node:22-alpine AS web
WORKDIR /web
COPY dashboard/package.json dashboard/package-lock.json ./
RUN npm ci
COPY dashboard/ ./
# The generated API client is committed; build straight to dist/.
RUN npm run build

# --- stage build: compile the Go binary with the SPA embedded ----------------
FROM golang:1.25-alpine AS build
RUN apk add --no-cache git
WORKDIR /src
COPY api/go.mod api/go.sum ./
RUN go mod download
COPY api/ ./
# Embed the freshly built SPA into the binary.
COPY --from=web /web/dist ./internal/webdist/static
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -tags embed_spa \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /platform ./cmd/platform

# --- stage runtime: small, debuggable image ----------------------------------
FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates wget && \
    adduser -D -u 10001 osctf
COPY --from=build /platform /platform
USER 10001
EXPOSE 8080
ENTRYPOINT ["/platform"]
CMD ["serve"]
