package scanner

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/chokevin/repograph/pkg/graph"
)

// FileEntry describes a single source file discovered during scanning.
type FileEntry struct {
	Path     string // relative to repo root
	Language string // detected language name
	Size     int64
}

// Options controls scanner behaviour.
type Options struct {
	MaxFileSize int64    // default 500 KB
	SkipDirs    []string // directories to skip entirely
	Extensions  []string // if set, only scan these extensions; empty = all registered
}

var defaultSkipDirs = []string{
	"node_modules", "vendor", ".git", "dist", "build", "__pycache__", ".outworked",
}

// Scan walks repoPath and returns all source files that have a registered
// language plugin, honouring .gitignore if present.
func Scan(repoPath string, opts *Options) ([]FileEntry, error) {
	if opts == nil {
		opts = &Options{}
	}
	if opts.MaxFileSize <= 0 {
		opts.MaxFileSize = 500 * 1024
	}
	skipDirs := opts.SkipDirs
	if len(skipDirs) == 0 {
		skipDirs = defaultSkipDirs
	}
	skipSet := make(map[string]bool, len(skipDirs))
	for _, d := range skipDirs {
		skipSet[d] = true
	}

	extFilter := make(map[string]bool, len(opts.Extensions))
	for _, e := range opts.Extensions {
		extFilter[e] = true
	}

	ignorePatterns := loadGitignore(repoPath)

	var entries []FileEntry

	err := filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, _ := filepath.Rel(repoPath, path)

		if info.IsDir() {
			if skipSet[info.Name()] {
				return filepath.SkipDir
			}
			if matchesGitignore(rel, true, ignorePatterns) {
				return filepath.SkipDir
			}
			return nil
		}

		if info.Size() > opts.MaxFileSize {
			return nil
		}

		if matchesGitignore(rel, false, ignorePatterns) {
			return nil
		}

		ext := filepath.Ext(info.Name())
		if len(extFilter) > 0 && !extFilter[ext] {
			return nil
		}

		lang := graph.LanguageForExtension(ext)
		if lang == "" {
			return nil
		}

		entries = append(entries, FileEntry{
			Path:     rel,
			Language: lang,
			Size:     info.Size(),
		})
		return nil
	})

	return entries, err
}

// gitignorePattern is a single parsed .gitignore line.
type gitignorePattern struct {
	pattern  string
	negated  bool
	dirOnly  bool
}

func loadGitignore(repoPath string) []gitignorePattern {
	f, err := os.Open(filepath.Join(repoPath, ".gitignore"))
	if err != nil {
		return nil
	}
	defer f.Close()

	var patterns []gitignorePattern
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := gitignorePattern{}
		if strings.HasPrefix(line, "!") {
			p.negated = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		p.pattern = line
		patterns = append(patterns, p)
	}
	return patterns
}

func matchesGitignore(relPath string, isDir bool, patterns []gitignorePattern) bool {
	matched := false
	for _, p := range patterns {
		if p.dirOnly && !isDir {
			continue
		}
		// Match against the full relative path and the base name.
		base := filepath.Base(relPath)
		if matchGlob(p.pattern, relPath) || matchGlob(p.pattern, base) {
			matched = !p.negated
		}
	}
	return matched
}

func matchGlob(pattern, name string) bool {
	ok, _ := filepath.Match(pattern, name)
	return ok
}
