# --- build stage --------------------------------------------------------
# This stage has the full Go toolchain. It compiles a binary and is then
# thrown away - none of it ends up in the image you actually run.
FROM golang:1-bookworm AS build
WORKDIR /src

# Copying just go.mod/go.sum first lets Docker cache the download step.
# As long as dependencies don't change, rebuilding after an app-code edit
# skips straight past "go mod download" instead of redoing it every time.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a statically linked binary with no dependency on
# system C libraries, which is what makes it runnable in the near-empty
# final image below.
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server

# --- final stage ----------------------------------------------------------
# alpine is a ~5MB Linux distribution - none of the Go toolchain, no source
# code, just enough OS to run one static binary. Compare this to the build
# stage, which is several hundred MB. Only this stage ships.
FROM alpine:3.20
COPY --from=build /out/server /server

EXPOSE 50051
ENTRYPOINT ["/server"]
