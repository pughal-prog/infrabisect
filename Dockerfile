# Build from source: docker build -t infrabisect .
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/infrabisect ./cmd/infrabisect

FROM alpine:3.20
# git for git sources; kubectl and helm for kind/helm sources and rollout checks.
RUN apk add --no-cache git kubectl helm ca-certificates
COPY --from=build /out/infrabisect /usr/local/bin/infrabisect
WORKDIR /work
ENTRYPOINT ["infrabisect"]
