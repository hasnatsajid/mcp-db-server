package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWriteJSONLines(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	l := New(&buf)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			if err := l.Write(Entry{Time: time.Unix(0, 0), Client: "test", Tool: "query", Rows: i}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	sc := bufio.NewScanner(&buf)
	n := 0
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", n, err)
		}
		n++
	}
	if n != 50 {
		t.Errorf("got %d lines, want 50", n)
	}
}

func TestOpenAndReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Write(Entry{Tool: "before"}); err != nil {
		t.Fatal(err)
	}
	rotated := path + ".1"
	if err := os.Rename(path, rotated); err != nil {
		t.Fatal(err)
	}
	if err := l.Reopen(); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(Entry{Tool: "after"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	for file, want := range map[string]string{rotated: "before", path: "after"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil || e.Tool != want {
			t.Errorf("%s: got %q (err %v), want tool %q", filepath.Base(file), b, err, want)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("audit log mode = %v, want 0600", perm)
	}
}
