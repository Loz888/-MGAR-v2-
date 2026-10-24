package policy

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/artifact"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
)

var ErrSandboxUnavailable = errors.New("NOT_RUN_SANDBOX_UNAVAILABLE")
var imageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Sandbox struct {
	Image     string
	Timeout   time.Duration
	MaxOutput int
}
type Isolation struct {
	User, Network                                            string
	Readonly, NoNewPrivileges, NoCapabilities, NoCredentials bool
	Memory, Pids, NanoCPUs                                   int64
}
type SandboxResult struct {
	verifier.TestExecution
	Isolation Isolation
	Removed   bool
}

func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--host=unix:///var/run/docker.sock"}, args...)...)
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		switch key {
		case "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH":
			continue
		}
		cmd.Env = append(cmd.Env, v)
	}
	return cmd
}
func (s Sandbox) Run(ctx context.Context, input verifier.TestInput) (verifier.TestExecution, error) {
	r, e := s.Execute(ctx, input)
	return r.TestExecution, e
}

// Execute is shared by Action experiments and independent Verifier replay.
// It transports pinned bytes through stdin, never a host directory or socket.
func (s Sandbox) Execute(parent context.Context, input verifier.TestInput) (SandboxResult, error) {
	result := SandboxResult{TestExecution: verifier.TestExecution{ExitCode: -1}}
	if !imageID.MatchString(s.Image) || s.Timeout <= 0 || s.Timeout > 30*time.Second || s.MaxOutput < 1 || s.MaxOutput > 1024*1024 {
		return result, ErrDenied
	}
	archive, e := sourceArchive(input)
	if e != nil {
		return result, e
	}
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return result, e
	}
	name := "mgar-sandbox-" + hex.EncodeToString(nonce)
	ctx, cancel := context.WithTimeout(parent, s.Timeout)
	defer cancel()
	args := []string{"run", "--name", name, "--label", "mgar.sandbox=" + name, "--pull=never", "--network=none", "--user=65532:65532", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--pids-limit=96", "--memory=768m", "--memory-swap=768m", "--cpus=2", "--ulimit", "nofile=256:256", "--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=512m,mode=1777", "--log-driver=none", "--interactive"}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		args = append(args, "--env", key+"=")
	}
	args = append(args, s.Image)
	output := &cappedOutput{limit: s.MaxOutput, cancel: cancel}
	stderr := &cappedOutput{limit: 16384, cancel: cancel}
	cmd := dockerCommand(ctx, args...)
	cmd.Stdin = bytes.NewReader(archive)
	cmd.Stdout = output
	cmd.Stderr = stderr
	runErr := cmd.Run()
	// The host inspects the owned container and then removes it even if the Docker
	// client was killed. No timeout path falls back to host command execution.
	inspectCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	info, inspectErr := dockerCommand(inspectCtx, "inspect", "--format", "{{json .}}", name).Output()
	stop()
	var state struct {
		Config struct {
			User   string
			Env    []string
			Labels map[string]string
		}
		HostConfig struct {
			NetworkMode                 string
			ReadonlyRootfs              bool
			CapDrop, SecurityOpt        []string
			Memory, PidsLimit, NanoCpus int64
		}
		Mounts []struct{ Type string }
		State  struct {
			Running, OOMKilled bool
			ExitCode           int
			Error              string
		}
	}
	if inspectErr != nil || json.Unmarshal(info, &state) != nil || state.Config.Labels["mgar.sandbox"] != name {
		result.Reason = "unavailable"
		// The name was freshly generated exclusively for this invocation. Even an
		// ambiguous daemon create response must be cleaned up before returning.
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		_ = dockerCommand(cleanup, "rm", "--force", "--volumes", name).Run()
		done()
		return result, ErrSandboxUnavailable
	}
	cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
	removeErr := dockerCommand(cleanup, "rm", "--force", "--volumes", name).Run()
	done()
	result.Removed = removeErr == nil
	isolated := Isolation{User: state.Config.User, Network: state.HostConfig.NetworkMode, Readonly: state.HostConfig.ReadonlyRootfs, Memory: state.HostConfig.Memory, Pids: state.HostConfig.PidsLimit, NanoCPUs: state.HostConfig.NanoCpus, NoCredentials: true}
	for _, v := range state.HostConfig.CapDrop {
		if v == "ALL" {
			isolated.NoCapabilities = true
		}
	}
	for _, v := range state.HostConfig.SecurityOpt {
		if v == "no-new-privileges:true" {
			isolated.NoNewPrivileges = true
		}
	}
	for _, v := range state.Config.Env {
		key, value, _ := strings.Cut(v, "=")
		switch key {
		case "PATH", "GOROOT", "GOCACHE", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "CGO_ENABLED", "GOMAXPROCS":
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy":
			if value != "" {
				isolated.NoCredentials = false
			}
		default:
			isolated.NoCredentials = false
		}
	}
	result.Isolation = isolated
	if !result.Removed || !isolated.NoCredentials || isolated.User != "65532:65532" || isolated.Network != "none" || !isolated.Readonly || !isolated.NoCapabilities || !isolated.NoNewPrivileges || len(state.Mounts) != 0 || isolated.Memory != 768*1024*1024 || isolated.Pids != 96 || isolated.NanoCPUs != 2e9 {
		result.Reason = "isolation_or_cleanup"
		return result, ErrSandboxUnavailable
	}
	// Preserve bounded partial stdout so timeout tests prove the payload began.
	result.Output = bytes.Clone(output.data)
	if _, body, found := bytes.Cut(output.data, []byte("\n")); found {
		result.Output = bytes.Clone(body)
	}
	if output.exceeded || stderr.exceeded {
		result.Reason = "output_limit"
		return result, nil
	}
	if ctx.Err() != nil {
		result.Reason = "timeout"
		return result, nil
	}
	if state.State.OOMKilled {
		result.Reason = "memory_limit"
		return result, nil
	}
	if state.State.Running || state.State.Error != "" {
		result.Reason = "unconfirmed"
		return result, fmt.Errorf("%w: running=%t runtime=%.2048s client=%.2048s", ErrSandboxUnavailable, state.State.Running, state.State.Error, stderr.data)
	}
	var exitErr *exec.ExitError
	if runErr != nil && (!errors.As(runErr, &exitErr) || exitErr.ExitCode() != state.State.ExitCode) {
		result.Reason = "transport"
		return result, ErrSandboxUnavailable
	}
	result.ExitCode = state.State.ExitCode
	header, body, found := bytes.Cut(output.data, []byte("\n"))
	var metadata struct {
		Toolchain string `json:"sandbox_toolchain"`
	}
	if !found || json.Unmarshal(header, &metadata) != nil || metadata.Toolchain == "" || len(stderr.data) != 0 || result.ExitCode == 125 {
		result.Reason = "runtime"
		return result, ErrSandboxUnavailable
	}
	result.Toolchain = metadata.Toolchain
	result.Output = bytes.Clone(body)
	return result, nil
}

type cappedOutput struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	remaining := w.limit - len(w.data)
	if len(p) > remaining {
		w.data = append(w.data, p[:remaining]...)
		w.exceeded = true
		w.cancel()
		return n, nil
	}
	w.data = append(w.data, p...)
	return n, nil
}

// Source bytes are read once through an anchored root and hashed while copying.
// The resulting archive owns its bytes; later source mutation cannot change it.
func sourceArchive(input verifier.TestInput) ([]byte, error) {
	root, e := os.OpenRoot(input.Directory)
	if e != nil {
		return nil, ErrDenied
	}
	defer root.Close()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	entries := map[string]string{}
	total := 0
	visited := 0
	e = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		visited++
		if visited > 1024 || len(path) > 4096 || strings.Count(path, "/") > 16 {
			return ErrDenied
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() || len(entries) >= 128 {
			return ErrDenied
		}
		f, e := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return e
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			return ErrDenied
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Nlink != 1 {
			return ErrDenied
		}
		b, e := io.ReadAll(io.LimitReader(f, 1024*1024+1))
		if e != nil || int64(len(b)) != info.Size() {
			return ErrDenied
		}
		total += len(b)
		if total > 4*1024*1024 {
			return ErrDenied
		}
		entries[path] = artifact.Digest(b)
		if e = tw.WriteHeader(&tar.Header{Name: path, Mode: 0600, Size: int64(len(b)), Typeflag: tar.TypeReg}); e != nil {
			return e
		}
		_, e = tw.Write(b)
		return e
	})
	if e != nil {
		return nil, e
	}
	if e = tw.Close(); e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(entries)
	if len(entries) == 0 || artifact.Digest(raw) != input.Digest {
		return nil, ErrDenied
	}
	return buf.Bytes(), nil
}

// SandboxTool binds an Action capability to controller-owned fixed inputs. No
// Agent argument is interpreted as a path, command, environment or Docker flag.
func SandboxTool(s Sandbox, inputs map[string]verifier.TestInput) Definition {
	frozen := map[string]verifier.TestInput{}
	resources := []string{}
	for k, v := range inputs {
		v.Tests = append([]string(nil), v.Tests...)
		frozen[k] = v
		resources = append(resources, k)
	}
	return Definition{Name: "sandbox_test", Version: "sandbox.v1", Role: "action", Effect: "idempotent_write", Resources: resources, Parameters: map[string]Parameter{}, Timeout: s.Timeout, MaxOutput: 65536, Execute: func(ctx context.Context, resource string, _ map[string]any) ([]byte, error) {
		input, ok := frozen[resource]
		if !ok {
			return nil, ErrDenied
		}
		result, e := s.Execute(ctx, input)
		if e != nil || result.Reason != "" {
			return nil, sandboxStopError{result.Removed}
		}
		return json.Marshal(map[string]any{"exit_code": result.ExitCode, "output_sha256": artifact.Digest(result.Output), "toolchain": result.Toolchain, "input_digest": input.Digest})
	}}
}

// The resource scheduler may release only after the host confirmed removal.
type sandboxStopError struct{ stopped bool }

func (e sandboxStopError) Error() string          { return ErrSandboxUnavailable.Error() }
func (e sandboxStopError) Unwrap() error          { return ErrSandboxUnavailable }
func (e sandboxStopError) ExecutionStopped() bool { return e.stopped }
