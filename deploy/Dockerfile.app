# Multi-target Dockerfile for the two Go binaries.
#   docker build -f deploy/Dockerfile.app --target gateway       -t llmgate/gateway:dev .
#   docker build -f deploy/Dockerfile.app --target mock-upstream -t llmgate/mock-upstream:dev .
# Build from the repo root. Run `go mod tidy` first so go.sum exists.

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mock-upstream ./cmd/mock-upstream

# distroless/static: no shell, no package manager, runs as non-root. Tiny attack surface.
FROM gcr.io/distroless/static-debian12:nonroot AS gateway
COPY --from=build /out/gateway /gateway
COPY data/pricing.seed.json /etc/llmgate/pricing.json
ENV PRICING_FILE=/etc/llmgate/pricing.json
EXPOSE 8080
ENTRYPOINT ["/gateway"]

FROM gcr.io/distroless/static-debian12:nonroot AS mock-upstream
COPY --from=build /out/mock-upstream /mock-upstream
EXPOSE 9000
ENTRYPOINT ["/mock-upstream"]
