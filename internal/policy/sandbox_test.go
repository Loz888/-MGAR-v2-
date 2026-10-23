package policy

import (
	"github.com/Loz888/-MGAR-v2-/internal/verifier"
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxSourceArchiveRejectsLinks(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "changed"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "input.txt")
			if e := os.WriteFile(p, []byte("fixed"), 0600); e != nil {
				t.Fatal(e)
			}
			digest, e := verifier.SourceDigest(dir)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "symlink" {
				e = os.Symlink(p, filepath.Join(dir, "link"))
			} else if kind == "hardlink" {
				e = os.Link(p, filepath.Join(dir, "link"))
			} else {
				e = os.WriteFile(p, []byte("changed"), 0600)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = sourceArchive(verifier.TestInput{Directory: dir, Digest: digest}); e == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}
