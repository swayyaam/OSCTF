FROM golang:1.25-alpine AS build
WORKDIR /src
COPY main.go .
RUN go mod init cookie-monster && CGO_ENABLED=0 go build -o /app main.go

FROM alpine:3.20
COPY --from=build /app /app
EXPOSE 8000
ENTRYPOINT ["/app"]
