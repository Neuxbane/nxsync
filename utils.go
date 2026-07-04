package main

import (
	"bufio"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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

			ignored := isIgnored(relPath, ignorePatterns)
			
			if ignored && !includeIgnored {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			if d.IsDir() {
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
	if relPath == "." || relPath == "" {
		return false
	}

	targetPath := filepath.ToSlash(relPath)

	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		pattern = filepath.ToSlash(pattern)

		if strings.HasSuffix(pattern, "/") {
			cleanDir := strings.TrimSuffix(pattern, "/")
			if targetPath == cleanDir || strings.HasPrefix(targetPath, pattern) || strings.Contains(targetPath, "/"+pattern) {
				return true
			}
		} else {
			if targetPath == pattern || filepath.Base(targetPath) == pattern {
				return true
			}
			if match, _ := filepath.Match(pattern, filepath.Base(targetPath)); match {
				return true
			}
		}
	}
	return false
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

// UPGRADED: Decodes custom binary schema file containing 16-byte MD5 path checksum blocks
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

// UPGRADED: Serializes metadata into low-level binary streams utilizing fixed 16-byte hashes
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
		hash := md5.Sum([]byte(relPath)) // Path conversion into fixed length of 16-bytes
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