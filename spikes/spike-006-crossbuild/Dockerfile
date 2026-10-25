# DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-002 §cross-build).
# One Dockerfile, one source tree, two platforms. Pattern identical to n8n-custom-service/Dockerfile (static Go on scratch).
# Build context = lab/drawio-custom-service/spikes
ARG GO_IMAGE=golang:1.27.2-alpine
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
WORKDIR /src
COPY spike-003-checksum/go.mod spike-003-checksum/main.go spike-003-checksum/
COPY spike-004-fuzz/go.mod spike-004-fuzz/*.go spike-004-fuzz/
COPY spike-005-pg/ spike-005-pg/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd spike-003-checksum && GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-s -w -buildid=" -o /out/canon . && \
    cd ../spike-004-fuzz   && GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-s -w -buildid=" -o /out/fuzz . && \
    cd ../spike-005-pg     && GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-s -w -buildid=" -o /out/pgbench .
RUN echo "app:x:65532:65532:app:/nonexistent:/sbin/nologin" > /out/passwd && echo "app:x:65532:" > /out/group

FROM scratch
COPY --from=build /out/canon /out/fuzz /out/pgbench /bin/
COPY --from=build /out/passwd /etc/passwd
COPY --from=build /out/group /etc/group
USER 65532:65532
ENTRYPOINT ["/bin/fuzz"]
