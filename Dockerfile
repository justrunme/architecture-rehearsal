# Production self-hosted change gate image (non-root).
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/rehearsal ./cmd/rehearsal

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/rehearsal /usr/local/bin/rehearsal
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/rehearsal"]
