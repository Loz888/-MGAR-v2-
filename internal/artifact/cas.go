// Package artifact stores bounded immutable bytes in a private local CAS.
package artifact

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
)

const MaxBytes = 1048576

var ErrIntegrity = errors.New("ARTIFACT_INTEGRITY_OR_SCOPE")
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Metadata struct {
	ID                string `json:"artifact_id"`
	RunID             string `json:"producer_run_id"`
	Kind              string `json:"kind"`
	Hash              string `json:"content_sha256"`
	StorageURI        string `json:"storage_uri"`
	SchemaVersion     string `json:"schema_version"`
	ProducerTaskID    string `json:"producer_task_id"`
	SourcePlanVersion int    `json:"source_plan_version"`
	ByteLength        int64  `json:"byte_length"`
	ContentType       string `json:"content_type"`
	ToolVersion       string `json:"tool_schema_version"`
	Verified          bool   `json:"-"`
}
type CAS struct {
	root *os.Root
	lock *os.File
	mu   sync.Mutex
	// Nil in production. The package crash test installs a deterministic
	// barrier after temp fsync and before rename; it grants no public hook.
	afterStage func()
}

func New(path string) (*CAS, error) {
	absolute, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	// Open/create each component relative to a pinned directory descriptor.
	// Checking symlinks after MkdirAll is too late: it can already have created
	// directories in a symlink target outside the requested private root.
	directory, e := os.OpenFile("/", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	defer func() { directory.Close() }()
	for _, component := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		if component == "" {
			continue
		}
		flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
		fd, err := syscall.Openat(int(directory.Fd()), component, flags, 0)
		if errors.Is(err, syscall.ENOENT) {
			if err = syscall.Mkdirat(int(directory.Fd()), component, 0700); err != nil && !errors.Is(err, syscall.EEXIST) {
				return nil, err
			}
			fd, err = syscall.Openat(int(directory.Fd()), component, flags, 0)
		}
		if err != nil {
			return nil, ErrIntegrity
		}
		next := os.NewFile(uintptr(fd), component)
		directory.Close()
		directory = next
	}
	info, e := directory.Stat()
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrIntegrity
	}
	root, e := os.OpenRoot(absolute)
	if e != nil {
		return nil, e
	}
	opened, e := root.Stat(".")
	if e != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, ErrIntegrity
	}
	lock, e := root.Open(".")
	if e != nil {
		root.Close()
		return nil, e
	}
	return &CAS{root: root, lock: lock}, nil
}
func (c *CAS) Close() error     { e := c.root.Close(); e2 := c.lock.Close(); return errors.Join(e, e2) }
func Digest(body []byte) string { h := sha256.Sum256(body); return hex.EncodeToString(h[:]) }

func (c *CAS) Read(hash string, size int64) ([]byte, error) {
	if !hashPattern.MatchString(hash) || size < 0 || size > MaxBytes {
		return nil, ErrIntegrity
	}
	f, e := c.root.OpenFile(hash, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, ErrIntegrity
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() != size {
		return nil, ErrIntegrity
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return nil, ErrIntegrity
	}
	body, e := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if e != nil || int64(len(body)) != size || Digest(body) != hash {
		return nil, ErrIntegrity
	}
	return body, nil
}
func (c *CAS) Put(body []byte) (string, error) {
	if len(body) > MaxBytes {
		return "", ErrIntegrity
	}
	body = bytes.Clone(body)
	hash := Digest(body)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := syscall.Flock(int(c.lock.Fd()), syscall.LOCK_EX); e != nil {
		return "", e
	}
	defer syscall.Flock(int(c.lock.Fd()), syscall.LOCK_UN)
	if _, e := c.root.Lstat(hash); e == nil {
		existing, e := c.Read(hash, int64(len(body)))
		if e != nil || !bytes.Equal(existing, body) {
			return "", ErrIntegrity
		}
		return hash, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	var nonce [16]byte
	if _, e := rand.Read(nonce[:]); e != nil {
		return "", e
	}
	temp := ".tmp-" + hex.EncodeToString(nonce[:])
	f, e := c.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", e
	}
	defer c.root.Remove(temp)
	if _, e = f.Write(body); e != nil {
		f.Close()
		return "", e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return "", e
	}
	if e = f.Close(); e != nil {
		return "", e
	}
	if c.afterStage != nil {
		c.afterStage()
	}
	if e = c.root.Rename(temp, hash); e != nil {
		return "", e
	}
	if e = c.lock.Sync(); e != nil {
		return "", e
	}
	return hash, nil
}

// Inventory never deletes: DB publication may be in flight or its outcome unknown.
// The trusted operator compares this physical inventory with immutable DB hashes.
func (c *CAS) Inventory() ([]string, error) {
	d, e := c.root.Open(".")
	if e != nil {
		return nil, e
	}
	defer d.Close()
	names, e := d.Readdirnames(10001)
	if e != nil && !errors.Is(e, io.EOF) {
		return nil, e
	}
	if len(names) > 10000 {
		return nil, ErrIntegrity
	}
	result := []string{}
	for _, name := range names {
		if hashPattern.MatchString(name) {
			info, e := c.root.Lstat(name)
			if e != nil {
				return nil, e
			}
			if _, e = c.Read(name, info.Size()); e != nil {
				return nil, e
			}
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result, nil
}

// StagingInventory reports interrupted temp files without deleting active writers.
func (c *CAS) StagingInventory() ([]string, error) {
	d, e := c.root.Open(".")
	if e != nil {
		return nil, e
	}
	defer d.Close()
	names, e := d.Readdirnames(10001)
	if e != nil && !errors.Is(e, io.EOF) {
		return nil, e
	}
	if len(names) > 10000 {
		return nil, ErrIntegrity
	}
	pending := []string{}
	for _, name := range names {
		if len(name) != 37 || !strings.HasPrefix(name, ".tmp-") || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(name[5:]) {
			continue
		}
		info, e := c.root.Lstat(name)
		if e != nil {
			return nil, e
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || st.Nlink != 1 || info.Size() > MaxBytes {
			return nil, ErrIntegrity
		}
		pending = append(pending, name)
	}
	sort.Strings(pending)
	return pending, nil
}
