package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

func handleDaemon(autoSync bool) {
	fmt.Println("[*] nxsync daemon: Starting background synchronization watcher...")
	
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Fatal: Failed to initialize fsnotify watcher: %v\n", err)
		os.Exit(1)
	}
	defer watcher.Close()

	// Read safe.conf to determine which directories to watch
	safePaths, err := readLines(SafeConf)
	if err != nil {
		fmt.Printf("[!] Warning: Could not read %s, daemon will only watch the current directory.\n", SafeConf)
		safePaths = []string{"."}
	}

	for _, path := range safePaths {
		trimmedPath := strings.TrimSpace(path)
		if trimmedPath == "" || strings.HasPrefix(trimmedPath, "#") {
			continue
		}
		
		absPath, err := filepath.Abs(trimmedPath)
		if err != nil {
			fmt.Printf("[!] Warning: Could not resolve absolute path for %s: %v\n", trimmedPath, err)
			continue
		}

		// fsnotify is not recursive by default. 
		// We need to walk the directory tree and add all subdirectories.
		err = filepath.Walk(absPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				// Skip .nxsync directory to avoid infinite loop when commit.bin is updated
				if filepath.Base(path) == ".nxsync" {
					return filepath.SkipDir
				}
				err = watcher.Add(path)
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			fmt.Printf("[!] Warning: Error walking path %s: %v\n", absPath, err)
		}
	}

	fmt.Printf("[*] Watching %d registered paths. Ready for changes...\n", len(safePaths))

	// Debounce mechanism: avoid triggering sync on every single write
	var (
		timer     *time.Timer
		mu        sync.Mutex
		syncMu    sync.Mutex
		debounce  = 2 * time.Second
	)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				mu.Lock()
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(debounce, func() {
					syncMu.Lock()
					defer syncMu.Unlock()
					
					absCwd, _ := filepath.Abs(".")
					relPath, err := filepath.Rel(absCwd, event.Name)
					if err != nil {
						fmt.Printf("[!] Error calculating relative path for %s: %v\n", event.Name, err)
						return
					}
					
					fmt.Printf("\n[!] Change detected at %s (%v). Recording commit...\n", relPath, event.Op)
					if err := commitSinglePath(relPath); err != nil {
						fmt.Printf("[!] Error committing path %s: %v\n", relPath, err)
					} else if autoSync {
						fmt.Println("[*] Auto-syncing changes to targets...")
						handleSync([]string{}, false, false)
					}
				})
				mu.Unlock()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "[!] Watcher error: %v\n", err)
		case <-sigChan:
			fmt.Println("\n[*] nxsync daemon: Shutting down...")
			return
		}
	}
}
