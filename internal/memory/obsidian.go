// Obsidian vault ingestion: scans a directory of markdown files,
// chunks them by heading sections, embeds, and stores in memory
// with kind="document" and source="obsidian:<path>".
//
// Config: PICOCLAW_OBSIDIAN_VAULT=/path/to/vault
//
// On startup: full scan (only new/modified files since last scan).
// Optionally: fsnotify watcher for live updates.
package memory

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ObsidianIngester scans an Obsidian vault and ingests markdown
// files into the memory store.
type ObsidianIngester struct {
	mem       *Memory
	vaultPath string
	chatFolder string // which chat's memory to store into
	log       *slog.Logger
}

// NewObsidianIngester creates an ingester for the given vault path.
func NewObsidianIngester(mem *Memory, vaultPath, chatFolder string, log *slog.Logger) *ObsidianIngester {
	if log == nil {
		log = slog.Default()
	}
	return &ObsidianIngester{
		mem:        mem,
		vaultPath:  vaultPath,
		chatFolder: chatFolder,
		log:        log,
	}
}

// Scan walks the vault, finds all .md files, chunks them by heading,
// and ingests new/modified chunks. Returns count of chunks ingested.
func (o *ObsidianIngester) Scan(ctx context.Context) (int, error) {
	if o.vaultPath == "" {
		return 0, nil
	}

	var ingested int
	err := filepath.Walk(o.vaultPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable files
		}
		if info.IsDir() {
			// Skip hidden directories (.obsidian, .git, etc.)
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			return nil
		}

		relPath, _ := filepath.Rel(o.vaultPath, path)
		n, err := o.ingestFile(ctx, path, relPath)
		if err != nil {
			o.log.Warn("obsidian: ingest failed",
				slog.String("file", relPath),
				slog.Any("err", err))
			return nil // continue scanning
		}
		ingested += n
		return nil
	})

	return ingested, err
}

// ingestFile reads a markdown file, splits into heading-based chunks,
// and stores each chunk that's new or modified.
func (o *ObsidianIngester) ingestFile(ctx context.Context, fullPath, relPath string) (int, error) {
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return 0, err
	}

	chunks := chunkByHeading(string(data), relPath)
	if len(chunks) == 0 {
		return 0, nil
	}

	var ingested int
	for _, chunk := range chunks {
		if len(chunk.Content) < 20 {
			continue // skip tiny chunks
		}

		source := "obsidian:" + relPath
		if chunk.Heading != "" {
			source += "#" + chunk.Heading
		}

		// Check if this exact content already exists (by hash in title).
		hash := contentHash(chunk.Content)
		title := fmt.Sprintf("%s [%s]", relPath, hash[:8])

		// Use dedup: if similar content exists (cosine > 0.92),
		// Memory.Add updates it instead of creating a duplicate.
		_, err := o.mem.Add(ctx, o.chatFolder, "document", title, chunk.Content, source)
		if err != nil {
			o.log.Debug("obsidian: chunk add failed",
				slog.String("file", relPath),
				slog.Any("err", err))
			continue
		}
		ingested++
	}

	return ingested, nil
}

// Chunk is a section of a markdown file.
type Chunk struct {
	Heading string // the heading text (empty for content before first heading)
	Content string // the full text including the heading line
}

// chunkByHeading splits markdown into chunks at ## headings.
// Each chunk includes the heading line + all content until the next
// heading of the same or higher level.
func chunkByHeading(md, filename string) []Chunk {
	var chunks []Chunk
	var current Chunk
	var currentLines []string

	scanner := bufio.NewScanner(strings.NewReader(md))
	for scanner.Scan() {
		line := scanner.Text()
		if isHeading(line) {
			// Save previous chunk.
			if len(currentLines) > 0 {
				current.Content = strings.Join(currentLines, "\n")
				chunks = append(chunks, current)
			}
			// Start new chunk.
			current = Chunk{Heading: strings.TrimLeft(line, "# ")}
			currentLines = []string{line}
		} else {
			currentLines = append(currentLines, line)
		}
	}
	// Save last chunk.
	if len(currentLines) > 0 {
		current.Content = strings.Join(currentLines, "\n")
		chunks = append(chunks, current)
	}

	// If there's only one chunk with no heading (whole file), use
	// filename as heading and limit to ~500 tokens (~2000 chars).
	if len(chunks) == 1 && chunks[0].Heading == "" {
		chunks[0].Heading = filename
		if len(chunks[0].Content) > 2000 {
			chunks[0].Content = chunks[0].Content[:2000]
		}
	}

	return chunks
}

func isHeading(line string) bool {
	return strings.HasPrefix(line, "# ") ||
		strings.HasPrefix(line, "## ") ||
		strings.HasPrefix(line, "### ")
}

func contentHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

// StartWatcher launches a background goroutine that re-scans the
// vault every interval. Simple polling — fsnotify can replace this
// later for lower latency.
func (o *ObsidianIngester) StartWatcher(ctx context.Context, interval time.Duration) {
	if o.vaultPath == "" || interval <= 0 {
		return
	}
	go func() {
		o.log.Info("obsidian watcher started",
			slog.String("vault", o.vaultPath),
			slog.Duration("interval", interval))

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, err := o.Scan(ctx)
				if err != nil {
					o.log.Warn("obsidian scan failed", slog.Any("err", err))
				} else if n > 0 {
					o.log.Info("obsidian: ingested chunks",
						slog.Int("count", n))
				}
			}
		}
	}()
}
