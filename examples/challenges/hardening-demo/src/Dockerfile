# The FLAG is injected by the platform at deploy time (this is a shared/static
# challenge, so every team sees the same flag). Not baked into the image.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY main.go .
RUN go mod init hardening-demo && CGO_ENABLED=0 go build -o /app main.go

FROM alpine:3.20
COPY --from=build /app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
