// The sandbox entrypoint is image-owned. It accepts a bounded source archive on
// stdin and executes one fixed Go Test command, without shell interpretation.
package main

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() { code := run(); os.Exit(code) }
func run() int {
	version, e := exec.Command("/usr/local/go/bin/go", "version").Output()
	if e != nil {
		return 125
	}
	header, _ := json.Marshal(map[string]string{"sandbox_toolchain": strings.TrimSpace(string(version))})
	fmt.Println(string(header))
	dir, e := os.MkdirTemp("/tmp", "job-")
	if e != nil {
		return 125
	}
	if e = copyCache("/opt/gocache", "/tmp/go-cache"); e != nil {
		return 125
	}
	tr := tar.NewReader(io.LimitReader(os.Stdin, 5*1024*1024))
	count := 0
	var total int64
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return 125
		}
		count++
		total += h.Size
		if count > 128 || total > 4*1024*1024 || h.Size < 0 || h.Size > 1024*1024 || h.Typeflag != tar.TypeReg || !fs.ValidPath(h.Name) || strings.Contains(h.Name, "\\") || len(h.Name) > 4096 {
			return 125
		}
		path := filepath.Join(dir, h.Name)
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return 125
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return 125
		}
		_, e = io.CopyN(f, tr, h.Size)
		closeErr := f.Close()
		if e != nil || closeErr != nil {
			return 125
		}
	}
	if count == 0 {
		return 125
	}
	cmd := exec.Command("/usr/local/go/bin/go", "test", "-count=1", "-json", "-vet=off", "-buildvcs=false", "./...")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/local/go/bin", "HOME=/tmp", "GOCACHE=/tmp/go-cache", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOVCS=*:off", "GOWORK=off", "CGO_ENABLED=0", "GOMAXPROCS=2", "TMPDIR=/tmp"}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	e = cmd.Run()
	if e == nil {
		return 0
	}
	if x, ok := e.(*exec.ExitError); ok {
		return x.ExitCode()
	}
	return 125
}
func copyCache(source, target string) error {
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(source, path)
		if e != nil {
			return e
		}
		out := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("invalid image cache")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		return os.WriteFile(out, b, 0600)
	})
}
