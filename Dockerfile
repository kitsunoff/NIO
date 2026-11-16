# Build the manager binary
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build
# the GOARCH has no default value to allow the binary to be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# The manager resolves mutable refs by running `git ls-remote`
# (internal/controller/nixresolve.go), so the runtime image needs git, plus ssh
# for ssh remotes. A distroless image has neither, and every NixCluster or
# workload that tracks a branch stalls with `exec: "git": executable file not
# found in $PATH`. The version calls fail the build if either tool is missing.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates git openssh-client \
    && git --version \
    && ssh -V
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
