# The FLAG is NOT baked in — the platform injects a per-team-unique flag at deploy
# time. Verify with `docker history` / `strings`: no flag appears in the image.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY main.go .
RUN go mod init per-team-web && CGO_ENABLED=0 go build -o /app main.go

FROM alpine:3.20
COPY --from=build /app /app
EXPOSE 8000
ENTRYPOINT ["/app"]
