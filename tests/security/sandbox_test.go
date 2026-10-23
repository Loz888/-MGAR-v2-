package security

import (
	"bytes"
	"context"
	"github.com/Loz888/-MGAR-v2-/internal/policy"
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sandboxFixture(t *testing.T) policy.Sandbox {
	t.Helper()
	raw, e := os.ReadFile("../../.local/sandbox-image.id")
	if e != nil {
		t.Fatal("NOT_RUN: build isolated image with scripts/build-sandbox.sh")
	}
	return policy.Sandbox{Image: strings.TrimSpace(string(raw)), Timeout: 30 * time.Second, MaxOutput: 1024 * 1024}
}
func sandboxInput(t *testing.T, source string) verifier.TestInput {
	t.Helper()
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.invalid/sandbox\n\ngo 1.27.3\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "probe_test.go"), []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	digest, e := verifier.SourceDigest(dir)
	if e != nil {
		t.Fatal(e)
	}
	return verifier.TestInput{Directory: dir, Digest: digest, Toolchain: "go version go1.27.3 linux/amd64", Package: "example.invalid/sandbox", Tests: []string{"TestSandboxProbe"}}
}
func TestSandboxIsolation(t *testing.T) {
	t.Setenv("MGAR_SANDBOX_SECRET_CANARY", "synthetic-only")
	hostCanary := filepath.Join(t.TempDir(), "host-only")
	if e := os.WriteFile(hostCanary, []byte("synthetic canary"), 0600); e != nil {
		t.Fatal(e)
	}
	source := `package sandbox
import("testing";"os";"strings";"net";"time")
func TestSandboxProbe(t *testing.T){
 if os.Getuid()!=65532{t.Fatal("root")}
 for _,path:=range []string{"/var/run/docker.sock","/workspace",` + `"` + hostCanary + `"` + `}{if _,e:=os.ReadFile(path);e==nil{t.Fatal("host mount exposed")}}
 for _,key:=range []string{"MGAR_TEST_DATABASE_URL","MGAR_TEST_DB_PASSWORD","OPENAI_API_KEY","MGAR_SANDBOX_SECRET_CANARY","HTTP_PROXY","HTTPS_PROXY","ALL_PROXY"}{if os.Getenv(key)!=""{t.Fatal("credential or proxy present")}}
 if e:=os.WriteFile("/root-write",[]byte("x"),0600);e==nil{t.Fatal("writable root")}
 if e:=os.WriteFile("/tmp/allowed",[]byte("x"),0600);e!=nil{t.Fatal(e)}
 status,e:=os.ReadFile("/proc/self/status");if e!=nil{t.Fatal(e)};if !strings.Contains(string(status),"CapEff:\t0000000000000000")||!strings.Contains(string(status),"NoNewPrivs:\t1")||!strings.Contains(string(status),"Seccomp:\t2"){t.Fatal("process privileges")}
 for _,address:=range []string{"192.0.2.1:443","127.0.0.1:55432"}{conn,e:=net.DialTimeout("tcp",address,200*time.Millisecond);if e==nil{conn.Close();t.Fatal("network accessible")}}
 t.Log("SANDBOX_ISOLATION_CONFIRMED")
}
`
	result, e := sandboxFixture(t).Execute(context.Background(), sandboxInput(t, source))
	if e != nil || result.Reason != "" || result.ExitCode != 0 || !result.Removed || !bytes.Contains(result.Output, []byte("SANDBOX_ISOLATION_CONFIRMED")) {
		t.Fatalf("sandbox result=%+v error=%v output=%s", result, e, result.Output)
	}
	if !result.Isolation.NoCredentials || !result.Isolation.NoNewPrivileges || result.Isolation.Memory == 0 || result.Isolation.Pids == 0 || result.Isolation.NanoCPUs == 0 {
		t.Fatal(result.Isolation)
	}
	if b, e := os.ReadFile(hostCanary); e != nil || string(b) != "synthetic canary" {
		t.Fatal("host changed")
	}
	t.Logf("actual isolated exit=%d limits=%+v", result.ExitCode, result.Isolation)
}
func TestSandboxTimeoutAndOutputCap(t *testing.T) {
	for _, mode := range []string{"timeout", "output_limit"} {
		t.Run(mode, func(t *testing.T) {
			s := sandboxFixture(t)
			// Include Docker startup and Go compilation; assert the payload actually
			// starts before judging its deadline, rather than timing startup alone.
			s.Timeout = 25 * time.Second
			code := `fmt.Println("PAYLOAD_STARTED");for {}`
			if mode == "output_limit" {
				s.MaxOutput = 4096
				code = `fmt.Println("PAYLOAD_STARTED");for {fmt.Println("0123456789abcdef0123456789abcdef0123456789abcdef")}`
			}
			input := sandboxInput(t, `package sandbox;import("testing";"fmt");func TestSandboxProbe(t *testing.T){`+code+`}`)
			start := time.Now()
			result, e := s.Execute(context.Background(), input)
			if e != nil || result.Reason != mode || !result.Removed || !bytes.Contains(result.Output, []byte("PAYLOAD_STARTED")) {
				t.Fatalf("%s actual=%+v error=%v output=%s", mode, result, e, result.Output)
			}
			if time.Since(start) > 40*time.Second {
				t.Fatal("unbounded cleanup")
			}
			t.Logf("owned container removed after %s; host test process alive", mode)
		})
	}
}
func TestSandboxRejectsUnfixedAndUnavailable(t *testing.T) {
	s := sandboxFixture(t)
	input := sandboxInput(t, `package sandbox;import "testing";func TestSandboxProbe(t *testing.T){}`)
	if e := os.WriteFile(filepath.Join(input.Directory, "probe_test.go"), []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Run(context.Background(), input); e == nil {
		t.Fatal("mutable source accepted")
	}
	s.Image = "sha256:" + strings.Repeat("0", 64)
	fresh := sandboxInput(t, `package sandbox;import "testing";func TestSandboxProbe(t *testing.T){}`)
	if _, e := s.Run(context.Background(), fresh); e != policy.ErrSandboxUnavailable {
		t.Fatal("missing Docker image did not block", e)
	}
}

func TestSandboxActionThroughGateway(t *testing.T) {
	s, c, cas := gatewayRoleFixture(t, "action")
	input := sandboxInput(t, `package sandbox;import "testing";func TestSandboxProbe(t *testing.T){t.Log("ACTION_TEST")}`)
	gateway, e := policy.NewGateway(s, cas, []policy.Definition{policy.SandboxTool(sandboxFixture(t), map[string]verifier.TestInput{"incident.tests": input})}, func(policy.Denial) {})
	if e != nil {
		t.Fatal(e)
	}
	token, e := gateway.Issue(context.Background(), c, []string{"incident.tests"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(gateway)
	defer server.Close()
	code, body := toolRequest(t, server.URL, token, `{"schema":"tool-call.v1","request_id":"55555555-5555-4555-8555-555555555555","tool":"sandbox_test","version":"sandbox.v1","resource":"incident.tests","arguments":{}}`)
	if code != 200 || !strings.Contains(body, `"exit_code":0`) {
		t.Fatal(code, body)
	}
	var n int
	if e = s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM tool_invocations WHERE status='succeeded' AND result_artifact_id IS NOT NULL").Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
