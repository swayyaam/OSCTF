# The FLAG is NOT baked in — the platform injects it at deploy time. Verify with
# `docker history` / `strings` on this image: no flag appears.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY main.go .
RUN go mod init env-hunter && CGO_ENABLED=0 go build -o /app main.go

FROM alpine:3.20
COPY --from=build /app /app
EXPOSE 8000
ENTRYPOINT ["/app"]
