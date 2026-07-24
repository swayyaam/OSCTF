# Compile the vulnerable binary and serve it over TCP with socat. The FLAG env
# is injected at deploy time and inherited by the socat-exec'd child.
FROM alpine:3.20 AS build
RUN apk add --no-cache gcc musl-dev
COPY vuln.c /vuln.c
RUN gcc -fno-stack-protector -no-pie -w -o /vuln /vuln.c

FROM alpine:3.20
RUN apk add --no-cache socat && adduser -D ctf
COPY --from=build /vuln /vuln
EXPOSE 9000
# fork per connection; the child inherits FLAG from the environment.
CMD ["socat", "-T", "60", "TCP-LISTEN:9000,reuseaddr,fork", "EXEC:/vuln,stderr"]
