// gopod-group-catalog imports an operator-selected metadata catalog. It never
// discovers an owner HOME, account credentials, Docker or Telegram endpoints.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Coriol-is/gopod/internal/nativecatalog"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = os.Stderr.WriteString("{\"status\":\"unavailable\"}\n")
		os.Exit(1)
	}
}
func run(args []string, w io.Writer) error {
	flags := flag.NewFlagSet("gopod-group-catalog", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "Explicit input metadata catalog file")
	output := flags.String("output", "", "Sanitized output file")
	defaultModel := flags.String("default-model", "", "Exact catalog model used as default")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *source == "" || *output == "" || *defaultModel == "" {
		return nativecatalog.ErrInvalid
	}
	sourcePath, err := filepath.Abs(*source)
	if err != nil {
		return nativecatalog.ErrInvalid
	}
	outputPath, err := filepath.Abs(*output)
	if err != nil || sourcePath == outputPath {
		return nativecatalog.ErrInvalid
	}
	f, err := os.OpenFile(sourcePath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nativecatalog.ErrInvalid
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > nativecatalog.MaxBytes {
		return nativecatalog.ErrInvalid
	}
	if dst, err := os.Stat(outputPath); err == nil && os.SameFile(st, dst) {
		return nativecatalog.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, nativecatalog.MaxBytes+1))
	if err != nil {
		return nativecatalog.ErrInvalid
	}
	catalog, err := nativecatalog.Sanitize(raw)
	if err != nil {
		return err
	}
	selected, err := catalog.Select("", *defaultModel)
	if err != nil {
		return err
	}
	if err = atomicWrite(outputPath, catalog.JSON); err != nil {
		return nativecatalog.ErrInvalid
	}
	return json.NewEncoder(w).Encode(struct {
		Status       string `json:"status"`
		Models       int    `json:"models"`
		DefaultModel string `json:"defaultModel"`
	}{"ok", len(catalog.Slugs), selected})
}
func atomicWrite(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".group-models-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err = temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}
