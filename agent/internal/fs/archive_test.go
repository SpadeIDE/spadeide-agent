package fsaccess

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestArchiveStem(t *testing.T) {
	cases := []struct{ in, want string }{
		{"foo.zip", "foo"},
		{"foo.tar", "foo"},
		{"foo.tar.gz", "foo"},
		{"foo.tgz", "foo"},
		{"foo.tar.bz2", "foo"},
		{"Archive.TAR.GZ", "Archive"},
		{"noext", "noext"},
	}
	for _, c := range cases {
		if got := archiveStem(c.in); got != c.want {
			t.Fatalf("archiveStem(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestUnsafeArchiveMember(t *testing.T) {
	bad := []string{"../etc/passwd", "/etc/passwd", `..\windows`, `C:\foo`, "a/../../b"}
	for _, n := range bad {
		if !unsafeArchiveMember(n) {
			t.Fatalf("expected unsafe: %q", n)
		}
	}
	good := []string{"ok.txt", "dir/file.txt", "dir/", "./rel.txt"}
	for _, n := range good {
		if unsafeArchiveMember(n) {
			t.Fatalf("expected safe: %q", n)
		}
	}
}

func TestUniqueSiblingNameTarGz(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "foo.tar.gz"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := uniqueSiblingName(dir, "foo", ".tar.gz"); got != "foo copy.tar.gz" {
		t.Fatalf("got %q", got)
	}
	if got := uniqueCopyName(dir, "bar"); got != "bar" {
		t.Fatalf("free name: %q", got)
	}
	if err := os.Mkdir(filepath.Join(dir, "bar"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := uniqueCopyName(dir, "bar"); got != "bar copy" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractZipAndArchiveRoundTrip(t *testing.T) {
	requireTool(t, "zip")
	requireTool(t, "unzip")

	root := t.TempDir()
	svc := newTestService(t, root)
	srcDir := filepath.Join(root, "proj")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	zipPath, err := svc.Archive(context.Background(), srcDir, "zip")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(zipPath) != "proj.zip" {
		t.Fatalf("zip name: %s", zipPath)
	}

	// Second compress should get unique name.
	zip2, err := svc.Archive(context.Background(), srcDir, "zip")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(zip2) != "proj copy.zip" {
		t.Fatalf("second zip: %s", zip2)
	}

	dest, err := svc.Extract(context.Background(), zipPath)
	if err != nil {
		t.Fatal(err)
	}
	// proj/ already exists → proj copy/
	if filepath.Base(dest) != "proj copy" {
		t.Fatalf("extract dest: %s", dest)
	}
	data, err := os.ReadFile(filepath.Join(dest, "hello.txt"))
	if err != nil {
		// zip -r may nest as proj/hello.txt
		data, err = os.ReadFile(filepath.Join(dest, "proj", "hello.txt"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if string(data) != "hi" {
		t.Fatalf("content %q", data)
	}
}

func TestExtractTarGzRoundTrip(t *testing.T) {
	requireTool(t, "tar")

	root := t.TempDir()
	svc := newTestService(t, root)
	src := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(src, []byte("note"), 0o644); err != nil {
		t.Fatal(err)
	}

	arc, err := svc.Archive(context.Background(), src, "tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(arc) != "notes.txt.tar.gz" {
		t.Fatalf("archive name: %s", arc)
	}

	dest, err := svc.Extract(context.Background(), arc)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dest) != "notes.txt copy" {
		t.Fatalf("extract dest: %s", dest)
	}
	got, err := os.ReadFile(filepath.Join(dest, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "note" {
		t.Fatalf("content %q", got)
	}
}

func TestExtractRejectsTraversalZip(t *testing.T) {
	requireTool(t, "zip")
	requireTool(t, "unzip")

	root := t.TempDir()
	svc := newTestService(t, root)
	// Build a zip with a sneaky member via zip's stdin absolute-name trick is hard;
	// create a zip from a crafted relative path with .. by invoking zip in a subdir.
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(filepath.Join(work, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(work, "a", "evil.txt")
	if err := os.WriteFile(evil, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "evil.zip")
	cmd := exec.Command("zip", "-q", zipPath, filepath.Join("..", "a", "evil.txt"))
	cmd.Dir = filepath.Join(work, "a")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zip: %v %s", err, out)
	}
	// Member may be stored as ../a/evil.txt
	_, err := svc.Extract(context.Background(), zipPath)
	if err == nil {
		t.Fatal("expected traversal refusal")
	}
}

func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH", name)
	}
}
