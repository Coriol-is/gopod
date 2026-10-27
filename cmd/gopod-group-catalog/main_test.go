package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/nativecatalog"
)

func TestExplicitImportAtomicPrivateOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.json")
	output := filepath.Join(dir, "models.json")
	raw := []byte(`{"models":[{"slug":"safe-model","tool_mode":"code_mode","base_instructions":"PRIVATE_INSTRUCTION"}],"etag":"PRIVATE_CACHE_VALUE"}`)
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := run([]string{"--source", source, "--output", output, "--default-model", "safe-model"}, &log); err != nil {
		t.Fatal(err)
	}
	if log.String() != `{"status":"ok","models":1,"defaultModel":"safe-model"}`+"\n" {
		t.Fatal("unsafe summary", log.String())
	}
	st, _ := os.Stat(output)
	if st.Mode().Perm() != 0600 {
		t.Fatal("output not private")
	}
	result, _ := os.ReadFile(output)
	if _, err := nativecatalog.ParseSanitized(result); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(source)
	if !bytes.Equal(after, raw) {
		t.Fatal("source changed")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("temporary file retained")
	}
}
func TestImportFailureNeverWritesOrPrintsSourceData(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "PRIVATE_source.json")
	output := filepath.Join(dir, "out.json")
	if err := os.WriteFile(source, []byte(`{"models":[{"slug":"safe"}],"auth":"PRIVATE_SECRET"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cases := [][]string{
		{}, {"--source", source, "--output", output},
		{"--source", source, "--output", source, "--default-model", "safe"},
		{"--source", source, "--output", output, "--default-model", "safe"},
		{"--PRIVATE_SECRET"},
	}
	for _, args := range cases {
		var log bytes.Buffer
		if run(args, &log) == nil {
			t.Fatal("invalid import accepted")
		}
		if log.Len() != 0 {
			t.Fatal("failure leaked data")
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatal("invalid import wrote output")
		}
	}
	raw := []byte(`{"models":[{"slug":"safe"}]}`)
	_ = os.WriteFile(source, raw, 0600)
	var log bytes.Buffer
	if run([]string{"--source", source, "--output", output, "--default-model", "PRIVATE_UNKNOWN"}, &log) == nil {
		t.Fatal("unknown default accepted")
	}
	if log.Len() != 0 {
		t.Fatal("selection failure leaked")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	if run([]string{"--source", link, "--output", output, "--default-model", "safe"}, &log) == nil {
		t.Fatal("symlink source accepted")
	}
	files, _ := os.ReadDir(dir)
	for _, entry := range files {
		if strings.HasPrefix(entry.Name(), ".group-models-") {
			t.Fatal("temp remains")
		}
	}
}

func TestFIFOInputRejectedWithoutWaitingForWriter(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.fifo")
	output := filepath.Join(dir, "models.json")
	if err := syscall.Mkfifo(source, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var log bytes.Buffer
		done <- run([]string{"--source", source, "--output", output, "--default-model", "safe"}, &log)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO source accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO import blocked before regular-file validation")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("FIFO wrote output")
	}
}
