package artifact

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCASDedupIntegrityAndInventory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cas")
	c, e := New(root)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	body := []byte("synthetic immutable finding")
	hash, e := c.Put(body)
	if e != nil {
		t.Fatal(e)
	}
	again, e := c.Put(body)
	if e != nil || again != hash {
		t.Fatal(again, e)
	}
	got, e := c.Read(hash, int64(len(body)))
	if e != nil || !bytes.Equal(got, body) {
		t.Fatal(e)
	}
	items, e := c.Inventory()
	if e != nil || len(items) != 1 || items[0] != hash {
		t.Fatal(items, e)
	}
	if e = os.WriteFile(filepath.Join(root, hash), []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Read(hash, int64(len(body))); e == nil {
		t.Fatal("corruption accepted")
	}
	if _, e = c.Put(body); e == nil {
		t.Fatal("existing corruption overwritten")
	}
	partial := ".tmp-0123456789abcdef0123456789abcdef"
	if e = os.WriteFile(filepath.Join(root, partial), []byte("interrupted staging"), 0600); e != nil {
		t.Fatal(e)
	}
	pending, e := c.StagingInventory()
	if e != nil || len(pending) != 1 || pending[0] != partial {
		t.Fatal(pending, e)
	}
	if _, e = os.Stat(filepath.Join(root, partial)); e != nil {
		t.Fatal("inventory deleted file", e)
	}
}

func TestCASPathSymlinkHardlinkAndBounds(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "cas")
	c, e := New(root)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	for _, name := range []string{"../escape", "/etc/passwd", "artifact://id", "", string(bytes.Repeat([]byte("A"), 64))} {
		if _, e = c.Read(name, 0); e == nil {
			t.Fatal("bad hash accepted", name)
		}
	}
	if _, e = c.Put(make([]byte, MaxBytes+1)); e == nil {
		t.Fatal("oversize accepted")
	}
	h, e := c.Put([]byte("test"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Link(filepath.Join(root, h), filepath.Join(base, "hard")); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Read(h, 4); e == nil {
		t.Fatal("hard link accepted")
	}
	if e = os.Remove(filepath.Join(base, "hard")); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(root, h)); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(filepath.Join(base, "missing"), filepath.Join(root, h)); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Read(h, 4); e == nil {
		t.Fatal("symlink accepted")
	}
	alias := filepath.Join(base, "alias")
	if e = os.Symlink(root, alias); e != nil {
		t.Fatal(e)
	}
	if _, e = New(alias); e == nil {
		t.Fatal("symlink root accepted")
	}
}
