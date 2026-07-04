package main

import (
	"bufio"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type IgnoreRule struct {
	Pattern    string
	Type       string // "file", "dir", or ""
	MaxBytes   int64  // -1 means no size restriction assigned
	IsExtended bool
}

// Converts standard byte threshold representations (B, KB, MB, GB) to raw int64 bytes
func parseSizeLimit(s string) int64 {
	s = strings.ToUpper(strings.TrimSpace(s))
	var multiplier int64 = 1
	if strings.HasSuffix(s, "GB") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	} else if strings.HasSuffix(s, "MB") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	} else if strings.HasSuffix(s, "KB") {
		multiplier = 1024
		s = strings.TrimSuffix(s, "KB")
	} else if strings.HasSuffix(s, "B") {
		s = strings.TrimSuffix(s, "B")
	}
	var val int64
	_, _ = fmt.Sscanf(s, "%d", &val)
	return val * multiplier
}

// Parses extended token configuration attributes line-by-line
func parseIgnoreRules(lines []string) []IgnoreRule {
	var rules []IgnoreRule
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		rule := IgnoreRule{
			Pattern:  parts[0],
			MaxBytes: -1,
		}
		if len(parts) > 1 {
			rule.IsExtended = true
			for _, part := range parts[1:] {
				if strings.HasPrefix(part, "type=") {
					rule.Type = strings.TrimPrefix(part, "type=")
				} else if strings.HasPrefix(part, "max=") {
					rule.MaxBytes = parseSizeLimit(strings.TrimPrefix(part, "max="))
				}
			}
		}
		rules = append(rules, rule)
	}
	return rules
}

// Evaluates whether a directory or file node meets current exclusion constraints
func shouldIgnorePath(relPath string, isDir bool, size int64, rules []IgnoreRule) bool {
	if relPath == "." || relPath == "" {
		return false
	}
	targetPath := filepath.ToSlash(relPath)

	for _, rule := range rules {
		// Enforce optional type constraint checks
		if rule.Type == "file" && isDir {
			continue
		}
		if rule.Type == "dir" && !isDir {
			continue
		}

		matched := false
		p := filepath.ToSlash(strings.TrimSuffix(rule.Pattern, "/"))

		if p == "*" {
			matched = true
		} else if targetPath == p || strings.HasPrefix(targetPath, p+"/") || strings.Contains(targetPath, "/"+p+"/") || strings.HasSuffix(targetPath, "/"+p) {
			matched = true
		} else if match, _ := filepath.Match(p, targetPath); match {
			matched = true
		} else if match, _ := filepath.Match(p, filepath.Base(targetPath)); match {
			matched = true
		}

		if matched {
			if rule.MaxBytes == -1 {
				return true // Absolute exclusion
			}
			if size > rule.MaxBytes {
				return true // Limit exceeded
			}
		}
	}
	return false
}

// Scans active safe paths to compute folder accumulation limits before running matrix comparisons
func precalculateFolderSizes(safePaths []string) (map[string]int64, map[string]int64) {
	fileSizes := make(map[string]int64)
	folderSizes := make(map[string]int64)
	absCwd, err := filepath.Abs(".")
	if err != nil {
		return fileSizes, folderSizes
	}

	for _, root := range safePaths {
		root = strings.TrimSpace(root)
		if root == "" || strings.HasPrefix(root, "#") {
			continue
		}
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}

		_ = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			absPath, _ := filepath.Abs(path)
			relPath, err := filepath.Rel(absCwd, absPath)
			if err != nil {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}

			sz := info.Size()
			fileSizes[relPath] = sz

			// Dynamically bubble-up size allocations into parent directories
			parent := filepath.Dir(relPath)
			for parent != "." && parent != "/" && parent != "" {
				folderSizes[parent] += sz
				parent = filepath.Dir(parent)
			}
			folderSizes["."] += sz
			return nil
		})
	}
	return fileSizes, folderSizes
}

func calculateDeltaMatrix(includeIgnored bool) (map[string]FileMeta, map[string]Change) {
	safePaths, _ := readLines(SafeConf)
	snapshot, _ := loadSnapshot(CommitDB)
	ignorePatterns, _ := readLines(IgnoreConf)
	currentFilesState := make(map[string]FileMeta)
	changesMap := make(map[string]Change)
	now := time.Now().Unix()

	absCwd, err := filepath.Abs(".")
	if err != nil {
		return currentFilesState, changesMap
	}

	// Generate lookup size boundaries and transform rule maps
	fileSizes, folderSizes := precalculateFolderSizes(safePaths)
	rules := parseIgnoreRules(ignorePatterns)

	for _, root := range safePaths {
		root = strings.TrimSpace(root)
		if root == "" || strings.HasPrefix(root, "#") {
			continue
		}
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}

		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}

		_ = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}

			absPath, err := filepath.Abs(path)
			if err != nil {
				return nil
			}

			relPath, err := filepath.Rel(absCwd, absPath)
			if err != nil {
				return nil
			}

			if strings.Contains(relPath, "targets.json") || strings.Contains(relPath, "commit.bin") || strings.Contains(relPath, "preview") {
				if d.IsDir() && strings.Contains(relPath, "preview") {
					return filepath.SkipDir
				}
				return nil
			}

			isDir := d.IsDir()
			var currentSize int64
			if isDir {
				currentSize = folderSizes[relPath]
			} else {
				currentSize = fileSizes[relPath]
			}

			ignored := shouldIgnorePath(relPath, isDir, currentSize, rules)
			
			if ignored && !includeIgnored {
				if isDir {
					return filepath.SkipDir // Optimize performance by skipping oversized sub-trees
				}
				return nil
			}

			if isDir {
				return nil
			}

			info, _ := d.Info()
			oldMeta, exists := snapshot[relPath]

			meta := FileMeta{
				Size: info.Size(),
				Mode: info.Mode(),
			}

			if ignored {
				meta.Timestamp = now
				changesMap[relPath] = Change{Path: relPath, Type: Ignored}
				return nil
			}

			if !exists {
				meta.Timestamp = now
				changesMap[relPath] = Change{Path: relPath, Type: Added}
			} else if oldMeta.Size != info.Size() || oldMeta.Mode != info.Mode() {
				meta.Timestamp = now
				changesMap[relPath] = Change{Path: relPath, Type: Modified}
			} else {
				meta.Timestamp = oldMeta.Timestamp 
			}

			currentFilesState[relPath] = meta
			return nil
		})
	}

	for oldPath := range snapshot {
		if isIgnored(oldPath, ignorePatterns) {
			continue
		}
		if _, exists := currentFilesState[oldPath]; !exists {
			changesMap[oldPath] = Change{Path: oldPath, Type: Deleted}
		}
	}

	return currentFilesState, changesMap
}

func isIgnored(relPath string, patterns []string) bool {
	rules := parseIgnoreRules(patterns)
	return shouldIgnorePath(relPath, false, 0, rules)
}

func pruneEmptyDirs(base, relDir string) {
	if relDir == "." || relDir == "" || relDir == string(filepath.Separator) {
		return
	}
	fullPath := filepath.Join(base, relDir)

	f, err := os.Open(fullPath)
	if err != nil {
		return
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	if err == io.EOF { 
		_ = os.Remove(fullPath)
		pruneEmptyDirs(base, filepath.Dir(relDir))
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, nil
}

func loadSnapshot(path string) (map[string]FileMeta, error) {
	m := make(map[string]FileMeta)
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()

	var count int32
	if err := binary.Read(f, binary.LittleEndian, &count); err != nil {
		if err == io.EOF {
			return m, nil
		}
		return m, err
	}

	for i := int32(0); i < count; i++ {
		var hash [16]byte
		if _, err := io.ReadFull(f, hash[:]); err != nil {
			return m, err
		}
		var size int64
		if err := binary.Read(f, binary.LittleEndian, &size); err != nil {
			return m, err
		}
		var timestamp int64
		if err := binary.Read(f, binary.LittleEndian, &timestamp); err != nil {
			return m, err
		}
		var mode uint32
		if err := binary.Read(f, binary.LittleEndian, &mode); err != nil {
			return m, err
		}
		var pathLen uint16
		if err := binary.Read(f, binary.LittleEndian, &pathLen); err != nil {
			return m, err
		}
		pathBytes := make([]byte, pathLen)
		if _, err := io.ReadFull(f, pathBytes); err != nil {
			return m, err
		}

		relPath := string(pathBytes)
		m[relPath] = FileMeta{
			Size:      size,
			Timestamp: timestamp,
			Mode:      os.FileMode(mode),
		}
	}
	return m, nil
}

func saveSnapshot(path string, m map[string]FileMeta) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := binary.Write(f, binary.LittleEndian, int32(len(m))); err != nil {
		return err
	}

	for relPath, meta := range m {
		hash := md5.Sum([]byte(relPath))
		if _, err := f.Write(hash[:]); err != nil {
			return err
		}
		_ = binary.Write(f, binary.LittleEndian, meta.Size)
		_ = binary.Write(f, binary.LittleEndian, meta.Timestamp)
		_ = binary.Write(f, binary.LittleEndian, uint32(meta.Mode))
		
		pathBytes := []byte(relPath)
		_ = binary.Write(f, binary.LittleEndian, uint16(len(pathBytes)))
		_, _ = f.Write(pathBytes)
	}
	return nil
}

func loadTargets(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return make(map[string]string), err
	}
	defer file.Close()
	b, _ := io.ReadAll(file)
	var m map[string]string
	_ = json.Unmarshal(b, &m)
	return m, nil
}

func saveTargets(path string, m map[string]string) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(path, b, 0644)
}