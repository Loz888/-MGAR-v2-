# Container execution boundary

Build the local image with `bash scripts/build-sandbox.sh`, then run
`bash scripts/test.sh --security-sandbox`. The full offline suite also requires
this image. Missing Docker or image causes a test failure reporting NOT_RUN;
it never enables a host fallback or passes via a skipped test.

The image uses scratch, the installed pinned Go toolchain, a static image-owned
entrypoint and a standard-library build cache. BuildKit runs with network=none;
no package download, credentials, shell or business database is included. Its
exact immutable image ID is saved to ignored `.local/sandbox-image.id`. Operators
supply that ID to `policy.Sandbox`. Rebuild after toolchain/entrypoint changes.
The script puts only Buildx state in `.local/buildx`, retaining managed Docker
configuration. Build contexts are private generated directories and are removed.

The host checks source types, link count, size, file count and input digest while
copying once into an owned archive. No host bind mounts are used. The container
receives source bytes on stdin and writes exclusively to a 512 MiB tmpfs. The
fixed command is `go test -count=1 -json -vet=off -buildvcs=false ./...`; network
module retrieval, CGO and automatic toolchain downloads are disabled. This is an
execution test, with repository `go vet` checked separately by the offline suite.

All runtime invocations require UID/GID 65532, read-only root, no capabilities,
no-new-privileges, Docker default seccomp, network=none, CPU=2, memory/swap=768 MiB,
PID limit=96, file descriptors=256 and deadline at most 30 seconds. Proxy variables
are explicitly empty; DB/model environment variables and sockets are absent.
The host bounds output and inspects the actual container state/configuration.
On timeout or excess output it cancels the Docker client, removes only its own
randomly named container, and confirms cleanup. Unconfirmed execution and failed
cleanup cannot become verified success. Partial stdout is evidence, not authority.

`SandboxTool` connects Action tool requests to the same executor through the
credential-bound Gateway. Verifier `Reexecutor` uses `Sandbox.Run` directly with
controller-fixed source/toolchain/oracle. Neither accepts an Agent Docker flag,
mount path, executable, environment or free-form shell command. No program runs
inside a long database transaction.

Security tests place all loop/output payloads inside the container. They assert
network/host mount denial, absent credentials, actual process UID/capabilities,
read-only root, timeout/output bounds, container removal and host process liveness.
The P5-02 buggy and corrected human fixtures are reexecuted through this boundary.
This is single-host Docker isolation, not a claim of resistance to kernel escape.
