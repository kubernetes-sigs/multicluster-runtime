# examples/sharded-namespace/Dockerfile
# Multi-stage to keep image slim
FROM golang:1.24 as build

WORKDIR /src
# Copy go.mod/sum from repo root so deps resolve; adjust paths if needed.
COPY go.mod go.sum ./
RUN go mod download

# Copy the whole repo to compile the example against local packages
COPY . .

# Build the example
WORKDIR /src/examples/sharded-namespace
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/sharded-example main.go

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/sharded-example /sharded-example
USER 65532:65532
ENTRYPOINT ["/sharded-example"]
