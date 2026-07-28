# The FLAG is NOT baked in — the platform injects a per-team-unique flag at deploy
# time. Verify with `strings`: no flag appears in the image.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY main.go .
RUN go mod init per-team-pwn && CGO_ENABLED=0 go build -o /app main.go

FROM alpine:3.20
COPY --from=build /app /app
EXPOSE 9000
ENTRYPOINT ["/app"]
