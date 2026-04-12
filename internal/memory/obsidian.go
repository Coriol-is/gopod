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
	"sync"
	"sync/atomic"
	"time"
)

// ObsidianIngester scans an Obsidian vault and ingests markdown
// files into the memory store. Tracks file mtimes to avoid
// re-embedding unchanged files.
type ObsidianIngester struct {
	mem        *Memory
	vaultPath  string
	chatFolder string
	log        *slog.Logger

	// indexed tracks path → mtime+size for change detection.
	mu      sync.Mutex
	indexed map[string]fileInfo
}

type fileInfo struct {
	ModTime int64 // unix seconds
	Size    int64
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
		indexed:    make(map[string]fileInfo),
	}
}

// Scan walks the vault, finds all .md files, chunks them by heading,
// and ingests new/modified chunks. Returns count of chunks ingested.
func (o *ObsidianIngester) Scan(ctx context.Context) (int, error) {
	if o.vaultPath == "" {
		return 0, nil
	}

	// Collect files to process (Walk is fast, embedding is slow).
	type fileJob struct {
		fullPath string
		relPath  string
		info     fileInfo
	}
	var jobs []fileJob
	var skipped int

	filepath.Walk(o.vaultPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			return nil
		}

		relPath, _ := filepath.Rel(o.vaultPath, path)
		current := fileInfo{ModTime: info.ModTime().Unix(), Size: info.Size()}

		if prev, ok := o.indexed[relPath]; ok {
			if prev.ModTime == current.ModTime && prev.Size == current.Size {
				skipped++
				return nil
			}
		}

		jobs = append(jobs, fileJob{fullPath: path, relPath: relPath, info: current})
		return nil
	})

	if skipped > 0 {
		o.log.Debug("obsidian: skipped unchanged files",
			slog.Int("skipped", skipped))
	}

	if len(jobs) == 0 {
		return 0, nil
	}

	// Process files in parallel (bounded concurrency).
	const workers = 5
	var ingested int64
	var wg sync.WaitGroup
	ch := make(chan fileJob, len(jobs))

	for _, j := range jobs {
		ch <- j
	}
	close(ch)

	for i := 0; i < workers && i < len(jobs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				n, err := o.ingestFile(ctx, j.fullPath, j.relPath)
				if err != nil {
					o.log.Warn("obsidian: ingest failed",
						slog.String("file", j.relPath),
						slog.Any("err", err))
					continue
				}
				atomic.AddInt64(&ingested, int64(n))
				// Track indexed file (thread-safe via mutex in caller
				// after wg.Wait, but safe here because each goroutine
				// handles a unique relPath).
				o.mu.Lock()
				o.indexed[j.relPath] = j.info
				o.mu.Unlock()
			}
		}()
	}
	wg.Wait()

	return int(ingested), nil
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

// chunkByHeading splits markdown into chunks at headings.
// Each chunk includes:
// - File frontmatter (YAML between --- delimiters) prepended as context
// - The heading line + content until next heading
// - Wiki links [[page]] resolved to "[see: page]" annotations
// - Overlap: last 2 lines of previous chunk prepended for continuity
func chunkByHeading(md, filename string) []Chunk {
	// Extract frontmatter if present.
	frontmatter := ""
	body := md
	if strings.HasPrefix(md, "---\n") {
		if end := strings.Index(md[4:], "\n---"); end >= 0 {
			frontmatter = md[4 : 4+end]
			body = strings.TrimSpace(md[4+end+4:])
		}
	}

	var chunks []Chunk
	var current Chunk
	var currentLines []string
	var prevTail []string // last 2 lines of previous chunk for overlap

	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if isHeading(line) {
			if len(currentLines) > 0 {
				current.Content = buildChunkContent(frontmatter, filename, prevTail, currentLines)
				chunks = append(chunks, current)
				// Save tail for overlap.
				prevTail = tailLines(currentLines, 2)
			}
			current = Chunk{Heading: strings.TrimLeft(line, "# ")}
			currentLines = []string{line}
		} else {
			currentLines = append(currentLines, line)
		}
	}
	if len(currentLines) > 0 {
		current.Content = buildChunkContent(frontmatter, filename, prevTail, currentLines)
		chunks = append(chunks, current)
	}

	if len(chunks) == 1 && chunks[0].Heading == "" {
		chunks[0].Heading = filename
	}

	// Resolve wiki links in all chunks.
	for i := range chunks {
		chunks[i].Content = resolveWikiLinks(chunks[i].Content)
		// Cap chunk size at ~2000 chars (~500 tokens).
		if len(chunks[i].Content) > 2000 {
			chunks[i].Content = chunks[i].Content[:2000]
		}
	}

	return chunks
}

// buildChunkContent assembles a chunk with optional frontmatter context
// and overlap from the previous chunk.
func buildChunkContent(frontmatter, filename string, overlap, lines []string) string {
	var parts []string
	if frontmatter != "" {
		parts = append(parts, "[file: "+filename+", metadata: "+compactFrontmatter(frontmatter)+"]")
	}
	if len(overlap) > 0 {
		parts = append(parts, "[..."+strings.Join(overlap, "\n")+"]")
	}
	parts = append(parts, strings.Join(lines, "\n"))
	return strings.Join(parts, "\n")
}

// compactFrontmatter extracts key fields from YAML frontmatter into
// a compact one-liner for the chunk context.
func compactFrontmatter(fm string) string {
	var pairs []string
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Keep only simple key: value pairs.
		if strings.Contains(line, ":") {
			pairs = append(pairs, line)
		}
	}
	result := strings.Join(pairs, "; ")
	if len(result) > 200 {
		result = result[:200]
	}
	return result
}

// resolveWikiLinks replaces [[page]] with [see: page] and
// [[page|alias]] with [see: alias (page)] so the agent knows
// there's a linked note without needing to parse wiki syntax.
func resolveWikiLinks(s string) string {
	result := s
	for {
		start := strings.Index(result, "[[")
		if start < 0 {
			break
		}
		end := strings.Index(result[start:], "]]")
		if end < 0 {
			break
		}
		end += start
		inner := result[start+2 : end]
		var replacement string
		if pipe := strings.Index(inner, "|"); pipe >= 0 {
			page := inner[:pipe]
			alias := inner[pipe+1:]
			replacement = "[see: " + alias + " (" + page + ")]"
		} else {
			replacement = "[see: " + inner + "]"
		}
		result = result[:start] + replacement + result[end+2:]
	}
	return result
}

func tailLines(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
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

// Reconcile archives memories whose source files no longer exist
// in the vault. Called after each Scan.
func (o *ObsidianIngester) Reconcile(ctx context.Context) (int64, error) {
	rows, err := o.mem.store.DB().QueryContext(ctx, `
		SELECT id, IFNULL(source,'') FROM memories
		 WHERE chat_folder = ? AND kind = 'document'
		   AND IFNULL(status,'active') = 'active'
		   AND source LIKE 'obsidian:%'`, o.chatFolder)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var archived int64
	now := time.Now().UnixMilli()
	for rows.Next() {
		var id int64
		var source string
		if err := rows.Scan(&id, &source); err != nil {
			continue
		}
		// source = "obsidian:path/to/file.md#heading"
		relPath := strings.TrimPrefix(source, "obsidian:")
		if hash := strings.Index(relPath, "#"); hash >= 0 {
			relPath = relPath[:hash]
		}
		fullPath := filepath.Join(o.vaultPath, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			o.mem.store.DB().ExecContext(ctx, `
				UPDATE memories SET status = 'archived', updated_at = ?
				 WHERE id = ?`, now, id)
			archived++
		}
	}
	return archived, nil
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
				// Reconcile: archive memories for deleted files.
				archived, _ := o.Reconcile(ctx)
				if archived > 0 {
					o.log.Info("obsidian: reconciled",
						slog.Int64("archived", archived))
				}
			}
		}
	}()
}
