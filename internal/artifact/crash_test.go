package artifact

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCASCrashProcessHelper(t *testing.T) {
	root := os.Getenv("MGAR_CAS_CRASH_TEST_ROOT")
	if root == "" {
		return
	}
	c, e := New(root)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.afterStage = func() {
		fmt.Println("STAGED")
		if _, e := bufio.NewReader(os.Stdin).ReadString('\n'); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = c.Put([]byte("actual fsynced but unpublished synthetic bytes")); e != nil {
		t.Fatal(e)
	}
}

func TestCASActualSIGKILLStagingInventory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cas")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCASCrashProcessHelper$")
	command.Env = append(os.Environ(), "MGAR_CAS_CRASH_TEST_ROOT="+root)
	output, e := command.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	input, e := command.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	defer input.Close()
	command.Stderr = os.Stderr
	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	reaped := false
	defer func() {
		if !reaped {
			command.Process.Kill()
			command.Wait()
		}
	}()
	barrier, e := bufio.NewReader(output).ReadString('\n')
	if e != nil || barrier != "STAGED\n" {
		t.Fatal("staging barrier", barrier, e)
	}
	if e = command.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	e = command.Wait()
	reaped = true
	exit, ok := e.(*exec.ExitError)
	if !ok {
		t.Fatal("child was not killed", e)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("expected real SIGKILL", status)
	}
	c, e := New(root)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	objects, e := c.Inventory()
	if e != nil || len(objects) != 0 {
		t.Fatal("partial object was published", objects, e)
	}
	staging, e := c.StagingInventory()
	if e != nil || len(staging) != 1 {
		t.Fatal("interrupted staging not inventoried", staging, e)
	}
	expected := []byte("actual fsynced but unpublished synthetic bytes")
	if _, e = c.Read(Digest(expected), int64(len(expected))); e == nil {
		t.Fatal("unpublished bytes readable as a CAS object")
	}
	t.Log("Confirmed child SIGKILL after real temp fsync; no published object; one preserved staging file; no destructive cleanup.")
}
