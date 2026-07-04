package main

import (
	"bufio"
	"fmt"
	"context"
	"time"
	"os"
	"io"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type SyncAction string

const (
	PushToTarget SyncAction = "PUSH"
	PullToLocal  SyncAction = "PULL"
	DeleteLocal  SyncAction = "DEL_LOCAL"
	DeleteTarget SyncAction = "DEL_TARGET"
)

type TargetPlan struct {
	Dest    string
	Actions map[string]SyncAction
}

type StorageClient struct {
	Protocol  string // "local", "sftp", "ftp"
	RawPath   string // Full raw target configuration string
	Host      string // host string for network operations (e.g., user@hostname)
	RemoteDir string // Target folder path on the destination endpoint
}

type TaskSlot struct {
	Path    string
	Action  SyncAction
	Current int64
	Total   int64
	Active  bool
}

func formatBytesPerSec(bytes float64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%.2f B/s", bytes)
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.2f KB/s", bytes/1024)
	} else if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.2f MB/s", bytes/(1024*1024))
	}
	return fmt.Sprintf("%.2f GB/s", bytes/(1024*1024*1024))
}

// Helper utility to parse target path string variables into protocol clients
func parseTargetURI(dest string) StorageClient {
	if strings.HasPrefix(dest, "sftp://") {
		trimmed := strings.TrimPrefix(dest, "sftp://")
		parts := strings.SplitN(trimmed, "/", 2)
		remoteDir := "/"
		if len(parts) == 2 {
			remoteDir += parts[1]
		}
		return StorageClient{Protocol: "sftp", RawPath: dest, Host: parts[0], RemoteDir: remoteDir}
	}
	if strings.HasPrefix(dest, "ftp://") {
		// Clean FTP endpoints using curl specifications
		return StorageClient{Protocol: "ftp", RawPath: dest, RemoteDir: dest}
	}
	return StorageClient{Protocol: "local", RawPath: dest}
}

// SAFETY CHECK: Validates reachability to prevent devastating data corruption loops
func isTargetReachable(client StorageClient) bool {
	switch client.Protocol {
	case "local":
		if _, err := os.Stat(client.RawPath); err != nil {
			if os.IsNotExist(err) {
				// If the folder itself doesn't exist, verify its parent folder exists.
				// This distinguishes a new folder setup from an unmounted network volume.
				parent := filepath.Dir(client.RawPath)
				if _, pErr := os.Stat(parent); pErr != nil {
					return false // Parent is missing or unreachable; safe skip triggered
				}
				return true // Valid uninitialized local subdirectory folder path
			}
			return false // Permission denied or hardware device disconnected error
		}
		return true

	case "sftp":
		// Probe remote host reachability with a strict 3-second connection timeout limit
		cmd := exec.Command("ssh", "-o", "ConnectTimeout=3", "-o", "BatchMode=yes", client.Host, "mkdir -p "+client.RemoteDir)
		return cmd.Run() == nil

	case "ftp":
		// Query the FTP directory listing index via curl to confirm connection stability
		cmd := exec.Command("curl", "--connect-timeout", "3", "--silent", "--fail", client.RemoteDir+"/")
		return cmd.Run() == nil
	}
	return false
}

func printPacmanBar(current, total int, currentFile string) {
	width := 25
	percent := float64(current) / float64(total)
	filled := int(percent * float64(width))

	// Animate mouth opening/closing based on step execution
	pacmanChar := "C"
	if current%2 == 0 {
		pacmanChar = "c"
	}
	if current == total {
		pacmanChar = "O"
	}

	var barStr string
	for i := 0; i < width; i++ {
		if i < filled {
			barStr += "#"
		} else if i == filled {
			if current == total {
				barStr += "#"
			} else {
				barStr += pacmanChar
			}
		} else {
			barStr += "."
		}
	}

	// Truncate filename if it's too long for cleaner UI alignment
	displayFile := currentFile
	if len(displayFile) > 20 {
		displayFile = "..." + displayFile[len(displayFile)-17:]
	} else if displayFile == "" {
		displayFile = "Initializing..."
	}

	fmt.Printf("\rProgress: [%-20s] [%s] %3d%% (%d/%d)", displayFile, barStr, int(percent*100), current, total)
}

func assertInWorkspace() {
	if _, err := os.Stat(StateDir); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "Fatal: not an nxsync workspace (or any of the parent directories): .nxsync")
		os.Exit(1)
	}
}

func handleInit() {
	if _, err := os.Stat(StateDir); err == nil {
		fmt.Println("Reinitialized existing nxsync workspace repository.")
		return
	}

	_ = os.MkdirAll(StateDir, 0755)
	_ = os.WriteFile(SafeConf, []byte("# Root directory boundaries to track. '.' maps to workspace root.\n.\n"), 0644)
	_ = os.WriteFile(IgnoreConf, []byte("# Explicit patterns to exclude from sync routines\n.git/\n"), 0644)
	_ = saveSnapshot(CommitDB, make(map[string]FileMeta)) // Seed blank binary ledger block
	_ = os.WriteFile(TargetsConf, []byte("{}"), 0644)

	pwd, _ := os.Getwd()
	fmt.Printf("[+] Initialized empty nxsync workspace in %s/\n", filepath.Join(pwd, StateDir))
}

func handleCommit() {
	fmt.Println("[*] Evaluation tracking commit triggered...")
	currentFilesState, changesMap := calculateDeltaMatrix(false)
	
	if len(changesMap) == 0 {
		fmt.Println("Everything is up-to-date. No new file mutations discovered.")
		return
	}

	// NEW: Redirect to log file if 20 or more changes exist
	if len(changesMap) >= 20 {
		logPath := filepath.Join(StateDir, "changes.log")
		fmt.Printf("\n[!] %d mutations discovered. Writing details to %s instead of terminal.\n", len(changesMap), logPath)
		
		var logLines []string
		logLines = append(logLines, "Recording Mutations to Commit Ledger:")
		for _, change := range changesMap {
			logLines = append(logLines, fmt.Sprintf("   [%s] %s", change.Type, change.Path))
		}
		_ = os.WriteFile(logPath, []byte(strings.Join(logLines, "\n")+"\n"), 0644)
	} else {
		fmt.Println("\nRecording Mutations to Commit Ledger:")
		for _, change := range changesMap {
			fmt.Printf("   [%s] %s\n", change.Type, change.Path)
		}
	}

	_ = saveSnapshot(CommitDB, currentFilesState)
	fmt.Println("\n[*] State index synchronization committed cleanly to commit.bin.")
}

func handleRestore(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Fatal: 'restore' requires a target destination argument.")
		fmt.Fprintln(os.Stderr, "Usage: nxsync restore <target-name>")
		os.Exit(1)
	}

	targets, _ := loadTargets(TargetsConf)
	targetName := args[0]
	targetDest, exists := targets[targetName]
	if !exists {
		fmt.Printf("Fatal: target node '%s' is not registered inside .nxsync/targets.json\n", targetName)
		os.Exit(1)
	}

	client := parseTargetURI(targetDest)

	// CRITICAL SAFETY BLOCK: Abort execution if destination target endpoint is dropped or unmounted
	if !isTargetReachable(client) {
		fmt.Printf("Fatal: Target Environment [%s] is unreachable. Restore aborted to preserve local files.\n", targetName)
		os.Exit(1)
	}

	ignorePatterns, _ := readLines(IgnoreConf)

	// Manage binary ledger indices retrieval workflows across local/remote networks
	var targetSnapshot map[string]FileMeta
	var targetInitialized bool

	if client.Protocol == "local" {
		targetCommitDB := filepath.Join(client.RawPath, CommitDB)
		_, err := os.Stat(targetCommitDB)
		targetInitialized = err == nil
		if targetInitialized {
			targetSnapshot, _ = loadSnapshot(targetCommitDB)
		}
	} else {
		// Remote Protocols: Extract commit ledger database to local system memory cache space
		tmpFile, err := os.CreateTemp("", "nxsync-restore-*.bin")
		if err == nil {
			tempLocalDB := tmpFile.Name()
			tmpFile.Close()
			
			var getCmd *exec.Cmd
			if client.Protocol == "sftp" {
				getCmd = exec.Command("scp", "-o", "ConnectTimeout=3", client.Host+":"+filepath.Join(client.RemoteDir, CommitDB), tempLocalDB)
			} else {
				getCmd = exec.Command("curl", "--connect-timeout", "3", "-s", "-f", "-o", tempLocalDB, client.RemoteDir+"/"+CommitDB)
			}

			if getCmd.Run() == nil {
				targetInitialized = true
				targetSnapshot, _ = loadSnapshot(tempLocalDB)
			}
			_ = os.Remove(tempLocalDB)
		}
	}

	if !targetInitialized {
		fmt.Printf("Fatal: target configuration location [%s] is not initialized with a valid commit.bin file.\n", targetName)
		os.Exit(1)
	}

	localLive, _ := calculateDeltaMatrix(false)

	restoreActions := make(map[string]SyncAction)
	allPaths := make(map[string]bool)
	for p := range localLive { allPaths[p] = true }
	for p := range targetSnapshot { allPaths[p] = true }

	for path := range allPaths {
		// Safeguard internal tracking configuration directories or ignored files
		if path == StateDir || strings.HasPrefix(path, StateDir+"/") || strings.HasPrefix(path, StateDir+string(filepath.Separator)) || strings.Contains(path, "targets.json") || strings.Contains(path, "commit.bin") || isIgnored(path, ignorePatterns) {
			continue
		}

		lMeta, lHasLive := localLive[path]
		tMeta, tHasSnap := targetSnapshot[path]

		if tHasSnap && !lHasLive {
			restoreActions[path] = PullToLocal
		} else if !tHasSnap && lHasLive {
			restoreActions[path] = DeleteLocal
		} else if tHasSnap && lHasLive {
			// OPTIMIZATION: Ignore timestamp alterations if file size and permission modes match perfectly
			if lMeta.Size != tMeta.Size || lMeta.Mode != tMeta.Mode {
				restoreActions[path] = PullToLocal
			}
		}
	}

	if len(restoreActions) == 0 {
		fmt.Printf("[*] Local environment already matches target layout [%s] perfectly.\n", targetName)
		return
	}

	// REDIRECTION: Divert console output list if actions hit a cumulative count of 20 or more files
	if len(restoreActions) >= 20 {
		logPath := filepath.Join(StateDir, "changes.log")
		fmt.Printf("\n[!] Compiled Destructive Restore Plan with %d actions. Writing details to %s instead of terminal.\n", len(restoreActions), logPath)
		
		var logLines []string
		logLines = append(logLines, fmt.Sprintf("[!] Compiled Destructive Restore Plan using Target Environment [%s]:", targetName))
		for path, action := range restoreActions {
			logLines = append(logLines, fmt.Sprintf("   [%s] %s", action, path))
		}
		_ = os.WriteFile(logPath, []byte(strings.Join(logLines, "\n")+"\n"), 0644)
	} else {
		fmt.Printf("\n[!] Compiled Destructive Restore Plan using Target Environment [%s]:\n", targetName)
		for path, action := range restoreActions {
			fmt.Printf("   [%s] %s\n", action, path)
		}
	}

	fmt.Print("\n[WARNING] This operation is destructive. Local changes will be lost.\nPress [ENTER] to execute restore checkout, or [CTRL+C] to abort...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')

	// Set up monitoring systems for cancellations
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	defer signal.Stop(sigChan)

	go func() {
		<-sigChan
		cancel()
	}()

	userInterrupted := false

	// --- ARCH LINUX MULTI-LINE PROGRESS SYSTEM ENGINE ---
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10) // Fixed window cap of 10 workers
	var mu sync.Mutex

	slots := make([]TaskSlot, 10)
	linesPrinted := 0
	globalTotal := len(restoreActions)
	globalCompleted := 0
	var globalBytes int64 = 0       // Track total transferred bytes
	startTime := time.Now()         // Track performance metrics timeline start

	// Local thread-safe terminal layout refresh function
	renderPacmanGrid := func() {
		mu.Lock()
		defer mu.Unlock()

		// Shift terminal cursor up vertically to rewrite matrix lines
		if linesPrinted > 0 {
			fmt.Printf("\033[%dA", linesPrinted)
		}
		linesPrinted = 0

		// Render each download/processing pipeline slot line in-place
		for i := 0; i < 10; i++ {
			slot := slots[i]
			if !slot.Active {
				fmt.Print("\033[K\n") // Clear empty spaces safely
				linesPrinted++
				continue
			}

			pct := 100
			if slot.Total > 0 {
				pct = int((float64(slot.Current) / float64(slot.Total)) * 100)
			}
			if pct > 100 { pct = 100 }

			// Draw ILoveCandy eating progress layout
			width := 20
			filled := int((float64(pct) / 100.0) * float64(width))
			barStr := ""
			for b := 0; b < width; b++ {
				if b < filled {
					barStr += "#"
				} else if b == filled {
					if pct == 100 {
						barStr += "#"
					} else if globalCompleted%2 == 0 {
						barStr += "C" // Mouth Open
					} else {
						barStr += "c" // Mouth Closed
					}
				} else {
					if b%2 == 0 { barStr += "o" } else { barStr += " " } // Candy dots
				}
			}

			displayPath := slot.Path
			if len(displayPath) > 22 {
				displayPath = "..." + displayPath[len(displayPath)-19:]
			}

			fmt.Printf("\033[K # Slot %d: [%-22s] [%s] %3d%% (%s)\n", i+1, displayPath, barStr, pct, slot.Action)
			linesPrinted++
		}

		// PERFORMANCE METRICS COMPUTATION
		elapsed := time.Since(startTime).Seconds()
		filesPerSec := 0.0
		bytesPerSec := 0.0
		if elapsed > 0 {
			filesPerSec = float64(globalCompleted) / elapsed
			bytesPerSec = float64(globalBytes) / elapsed
		}

		// Render transfer throughput line directly above total restore progress line
		fmt.Printf("\033[K Throughput Performance: %.2f files/s | %s\n", filesPerSec, formatBytesPerSec(bytesPerSec))
		linesPrinted++

		globalPct := int((float64(globalCompleted) / float64(globalTotal)) * 100)
		fmt.Printf("\033[K Total Restore Progress: %d/%d files completed [%d%%]\n", globalCompleted, globalTotal, globalPct)
		linesPrinted++
	}

	// Deploy initial progress grid view state
	renderPacmanGrid()

	for path, action := range restoreActions {
		if ctx.Err() != nil {
			userInterrupted = true
			break
		}

		tMeta := targetSnapshot[path]

		wg.Add(1)
		sem <- struct{}{} // Allocate concurrency processing space block

		// Claim open worker line row index safely
		mu.Lock()
		slotIdx := -1
		for i := 0; i < 10; i++ {
			if !slots[i].Active {
				slotIdx = i
				break
			}
		}
		slots[slotIdx] = TaskSlot{
			Path:   path,
			Action: action,
			Active: true,
		}
		if action == PullToLocal {
			slots[slotIdx].Total = tMeta.Size
		} else {
			slots[slotIdx].Total = 1 // Basic structural metadata state
		}
		mu.Unlock()
		renderPacmanGrid()

		go func(p string, act SyncAction, tm FileMeta, targetSlot int) {
			cleanCompletion := true
			defer func() {
				mu.Lock()
				slots[targetSlot].Active = false
				if cleanCompletion {
					globalCompleted++
				}
				mu.Unlock()
				renderPacmanGrid()
				<-sem
				wg.Done()
			}()

			targetFullPath := filepath.Join(client.RemoteDir, p)

			switch act {
			case PullToLocal:
				_ = os.MkdirAll(filepath.Dir(p), 0755)
				if client.Protocol == "local" {
					localSrc := filepath.Join(client.RawPath, p)
					srcFile, err := os.Open(localSrc)
					if err == nil {
						dstFile, err := os.Create(p)
						if err == nil {
							buf := make([]byte, 32*1024)
							for {
								if ctx.Err() != nil { cleanCompletion = false; break }
								n, rErr := srcFile.Read(buf)
								if n > 0 {
									_, _ = dstFile.Write(buf[:n])
									mu.Lock()
									slots[targetSlot].Current += int64(n)
									globalBytes += int64(n) // Aggregate global throughput metrics
									mu.Unlock()
									renderPacmanGrid() 
								}
								if rErr == io.EOF { break }
							}
							dstFile.Close()
						}
						srcFile.Close()
						if !cleanCompletion { _ = os.Remove(p) } else { _ = os.Chmod(p, tm.Mode) }
					}
				} else if client.Protocol == "sftp" {
					cmd := exec.Command("scp", client.Host+":"+targetFullPath, p)
					if cmd.Run() == nil {
						_ = os.Chmod(p, tm.Mode)
						mu.Lock(); slots[targetSlot].Current = tm.Size; globalBytes += tm.Size; mu.Unlock()
					} else { cleanCompletion = false }
				} else if client.Protocol == "ftp" {
					cmd := exec.Command("curl", "-o", p, client.RemoteDir+"/"+p)
					if cmd.Run() == nil {
						_ = os.Chmod(p, tm.Mode)
						mu.Lock(); slots[targetSlot].Current = tm.Size; globalBytes += tm.Size; mu.Unlock()
					} else { cleanCompletion = false }
				}

				if cleanCompletion {
					mu.Lock()
					localLive[p] = tm
					mu.Unlock()
				}

			case DeleteLocal:
				if ctx.Err() == nil {
					_ = os.Remove(p)
					pruneEmptyDirs(".", filepath.Dir(p))
					mu.Lock()
					slots[targetSlot].Current = 1
					delete(localLive, p)
					mu.Unlock()
				} else {
					cleanCompletion = false
				}
			}
		}(path, action, tMeta, slotIdx) // Fixed: Scoped variable mapped correctly to 'tMeta'
	}

	wg.Wait()
	linesPrinted = 0 // Unlock screen space bounds safely for tracking lines exit summary
	fmt.Println()

	// Write changes directly back down into local binary ledger definitions cleanly
	_ = saveSnapshot(CommitDB, localLive)

	if userInterrupted {
		fmt.Fprintln(os.Stderr, "\n[!] Operation aborted mid-execution by user. Safe states maintained.")
		os.Exit(130)
	} else {
		fmt.Println("[*] Restore configuration checkout operation completed successfully.")
	}
}

func handleTarget(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: nxsync target add <name> <path/URI>")
		fmt.Println("       nxsync target list")
		os.Exit(1)
	}

	switch args[0] {
	case "add":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "Fatal: 'target add' requires both a target nickname name and a destination path/URI.")
			fmt.Fprintln(os.Stderr, "Example: nxsync target add backup-nas sftp://user@192.168.1.50/volume/backup")
			os.Exit(1)
		}

		name := args[1]
		dest := args[2]

		// Safeguard internal infrastructure keywords
		if name == "all" {
			fmt.Fprintln(os.Stderr, "Fatal: 'all' is a reserved system keyword and cannot be used as a target name.")
			os.Exit(1)
		}

		// PARSE & VALIDATE TARGET REACHABILITY BEFORE SAVING
		client := parseTargetURI(dest)
		fmt.Printf("[*] Probing reachability pipeline for target: %s...\n", dest)
		
		if !isTargetReachable(client) {
			fmt.Printf("Fatal: Target destination '%s' is completely unreachable or non-existent.\n", dest)
			fmt.Println("[!] Target registration aborted to prevent downstream database or tracking state drops.")
			os.Exit(1)
		}

		// Target is validated and online, proceed with saving to settings
		targets, err := loadTargets(TargetsConf)
		if err != nil || targets == nil {
			targets = make(map[string]string)
		}

		targets[name] = dest
		if err := saveTargets(TargetsConf, targets); err != nil {
			fmt.Fprintf(os.Stderr, "Fatal: Failed to write configuration updates to targets.json: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("[+] Target Node [%s] validated and successfully registered to tracking map.\n", name)

	case "list":
		targets, _ := loadTargets(TargetsConf)
		if len(targets) == 0 {
			fmt.Println("[*] No active target destinations are registered inside .nxsync/targets.json")
			return
		}

		fmt.Println("[*] Registered Remote & Local Storage Targets:")
		for name, dest := range targets {
			client := parseTargetURI(dest)
			// Optional: Display a visual status label alongside items
			status := "ONLINE/VALID"
			if !isTargetReachable(client) {
				status = "UNREACHABLE/OFFLINE"
			}
			fmt.Printf("   %-15s -> %-50s [%s]\n", name, dest, status)
		}

	default:
		fmt.Printf("nxsync target: unknown management command '%s'\n", args[0])
		fmt.Println("See 'nxsync --help' for proper structural usage instructions.")
		os.Exit(1)
	}
}

func handlePreview() {
	currentFilesState, changesMap := calculateDeltaMatrix(false) 
	rootTree := &TreePath{Children: make(map[string]*TreePath)}

	addIntoTree := func(relPath string) {
		parts := strings.Split(filepath.ToSlash(relPath), "/")
		current := rootTree
		
		isDir := false
		if info, err := os.Stat(relPath); err == nil {
			isDir = info.IsDir()
		} else {
			if strings.HasSuffix(relPath, "/") {
				isDir = true
			}
		}

		for i, part := range parts {
			if part == "" || part == "." {
				continue
			}
			isLast := i == len(parts)-1
			
			child, exists := current.Children[part]
			if !exists {
				child = &TreePath{
					Name:     part,
					IsDir:    !isLast || isDir,
					Children: make(map[string]*TreePath),
				}
				current.Children[part] = child
			}
			current = child
		}
	}

	for path := range currentFilesState {
		addIntoTree(path)
	}
	for path := range changesMap {
		addIntoTree(path)
	}

	mountPoint := filepath.Join(StateDir, "preview")
	
	_, _ = exec.Command("fusermount3", "-u", mountPoint).Output()
	_ = os.MkdirAll(mountPoint, 0755)

	rootNode := &PreviewNode{
		Children: rootTree.Children,
	}
	
	server, err := fs.Mount(mountPoint, rootNode, &fs.Options{
		MountOptions: fuse.MountOptions{
			Name: "nxsync-preview",
		},
	})
	if err != nil {
		fmt.Printf("Failed to mount FUSE overlay: %v\n", err)
		return
	}

	fmt.Printf("[*] Local configuration preview FUSE mounted at %s\n", mountPoint)
	fmt.Println("[!] Open another terminal window to inspect rules mapping. No snapshot will be committed.")
	fmt.Println("Press [ENTER] to tear down preview matrix mount...")

	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	_ = server.Unmount()
	_, _ = exec.Command("fusermount3", "-u", mountPoint).Output()
	if err := os.RemoveAll(mountPoint); err != nil {
		fmt.Printf("Failed to cleanup preview directory: %v\n", err)
	}
	fmt.Println("[*] Preview unmounted cleanly.")
}

func handleSync(args []string) {
	targets, _ := loadTargets(TargetsConf)
	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "Fatal: No sync target destinations are registered inside .nxsync/targets.json")
		os.Exit(1)
	}

	var targetsToSync map[string]string
	syncAll := len(args) == 0 || args[0] == "all"

	if syncAll {
		targetsToSync = targets
		fmt.Println("[*] Strategy Evaluation: Evaluating ALL registered target ledgers.")
	} else {
		targetName := args[0]
		path, exists := targets[targetName]
		if !exists {
			fmt.Printf("Fatal: target node '%s' is not registered inside .nxsync/targets.json\n", targetName)
			os.Exit(1)
		}
		targetsToSync = map[string]string{targetName: path}
	}

	localSnapshot, _ := loadSnapshot(CommitDB)

	fmt.Println("[*] Auto-committing local workspace changes...")
	localLive, localChanges := calculateDeltaMatrix(false)
	if len(localChanges) > 0 {
		fmt.Printf("[+] Auto-commit finalized. Detected %d structural changes. Updating local ledger...\n", len(localChanges))
		_ = saveSnapshot(CommitDB, localLive)
	}

	allPlans := make(map[string]TargetPlan)
	targetClients := make(map[string]StorageClient) // Maps active system URI configurations
	hasAnyActions := false

	ignorePatterns, _ := readLines(IgnoreConf)

	for name, targetDest := range targetsToSync {
		client := parseTargetURI(targetDest)
		
		// CRITICAL SAFETY BLOCK: Abort execution if destination target endpoint drops
		if !isTargetReachable(client) {
			fmt.Printf("[!] Warning: Target [%s] path is unreachable or doesn't exist. Skipped to prevent rolling back data.\n", name)
			continue
		}
		targetClients[name] = client

		// Manage binary indices retrieval workflows across local/remote networks
		var targetSnapshot map[string]FileMeta
		var targetInitialized bool
		var tempLocalDB string

		if client.Protocol == "local" {
			targetCommitDB := filepath.Join(client.RawPath, CommitDB)
			_, err := os.Stat(targetCommitDB)
			targetInitialized = err == nil
			if targetInitialized {
				targetSnapshot, _ = loadSnapshot(targetCommitDB)
			}
		} else {
			// Remote Protocols: Extract commit ledger database to local system memory cache space
			tmpFile, err := os.CreateTemp("", "nxsync-remote-*.bin")
			if err == nil {
				tempLocalDB = tmpFile.Name()
				tmpFile.Close()
				
				var getCmd *exec.Cmd
				if client.Protocol == "sftp" {
					getCmd = exec.Command("scp", "-o", "ConnectTimeout=3", client.Host+":"+filepath.Join(client.RemoteDir, CommitDB), tempLocalDB)
				} else {
					getCmd = exec.Command("curl", "--connect-timeout", "3", "-s", "-f", "-o", tempLocalDB, client.RemoteDir+"/"+CommitDB)
				}

				if getCmd.Run() == nil {
					targetInitialized = true
					targetSnapshot, _ = loadSnapshot(tempLocalDB)
				} else {
					targetInitialized = false // Target layout repository evaluated as uninitialized block
				}
				_ = os.Remove(tempLocalDB)
			}
		}

		actions := make(map[string]SyncAction)

		if !targetInitialized {
			for path := range localLive {
				if strings.Contains(path, "targets.json") || strings.Contains(path, "commit.bin") || strings.Contains(path, "preview") || isIgnored(path, ignorePatterns) {
					continue
				}
				actions[path] = PushToTarget
			}
			if len(actions) > 0 {
				allPlans[name] = TargetPlan{Dest: targetDest, Actions: actions}
				hasAnyActions = true
			}
			continue 
		}

		allPaths := make(map[string]bool)
		for p := range localLive { allPaths[p] = true }
		for p := range localSnapshot { allPaths[p] = true }
		for p := range targetSnapshot { allPaths[p] = true }

		for path := range allPaths {
			if strings.Contains(path, "targets.json") || strings.Contains(path, "commit.bin") || strings.Contains(path, "preview") || isIgnored(path, ignorePatterns) {
				continue
			}

			lMeta, lHasLive := localLive[path]
			_, lHasSnap := localSnapshot[path]
			tMeta, tHasSnap := targetSnapshot[path]

			if lHasLive && tHasSnap {
				if lMeta.Size == tMeta.Size && lMeta.Mode == tMeta.Mode {
					continue
				}
				if lMeta.Timestamp > tMeta.Timestamp {
					actions[path] = PushToTarget
				} else if tMeta.Timestamp > lMeta.Timestamp {
					actions[path] = PullToLocal
				}
				continue
			}

			if !lHasLive && tHasSnap {
				if lHasSnap {
					actions[path] = DeleteTarget 
				} else {
					actions[path] = PullToLocal  
				}
				continue
			}

			if lHasLive && !tHasSnap {
				if !lHasSnap {
					actions[path] = PushToTarget 
				} else {
					actions[path] = DeleteLocal  
				}
				continue
			}
		}

		if len(actions) > 0 {
			allPlans[name] = TargetPlan{Dest: targetDest, Actions: actions}
			hasAnyActions = true
		}
	}

	if !hasAnyActions {
		fmt.Println("[*] All target configurations are perfectly synchronized with commit.bin ledgers.")
		return
	}

	totalActions := 0
	for _, plan := range allPlans {
		totalActions += len(plan.Actions)
	}

	if totalActions >= 20 {
		logPath := filepath.Join(StateDir, "changes.log")
		fmt.Printf("\n[!] Global Multi-Target Sync Plan Compiled with %d actions. Writing details to %s instead of terminal.\n", totalActions, logPath)
		
		var logLines []string
		logLines = append(logLines, "[*] Global Multi-Target Sync Plan Compiled:")
		for name, plan := range allPlans {
			logLines = append(logLines, fmt.Sprintf("\nTarget Location [%s] -> %s:", name, plan.Dest))
			for path, action := range plan.Actions {
				logLines = append(logLines, fmt.Sprintf("   [%s] %s", action, path))
			}
		}
		_ = os.WriteFile(logPath, []byte(strings.Join(logLines, "\n")+"\n"), 0644)
	} else {
		fmt.Println("\n[*] Global Multi-Target Sync Plan Compiled:")
		for name, plan := range allPlans {
			fmt.Printf("\nTarget Location [%s] -> %s:\n", name, plan.Dest)
			for path, action := range plan.Actions {
				fmt.Printf("   [%s] %s\n", action, path)
			}
		}
	}

	fmt.Print("\nPress [ENTER] to execute all planned changes across workspace boundaries, or [CTRL+C] to abort...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	defer signal.Stop(sigChan)

	go func() {
		<-sigChan
		cancel()
	}()

	userInterrupted := false

	for name, plan := range allPlans {
		if ctx.Err() != nil {
			userInterrupted = true
			break
		}

		client := targetClients[name]
		fmt.Printf("[*] Synchronizing target pipeline context [%s]...\n", name)
		
		// Fetch snapshot memory references over local/remote configurations
		targetCommitDB := filepath.Join(client.RawPath, CommitDB)
		var targetSnapshot map[string]FileMeta
		
		if client.Protocol == "local" {
			targetSnapshot, _ = loadSnapshot(targetCommitDB)
		} else {
			tmpFile, _ := os.CreateTemp("", "nxsync-sync-*.bin")
			tempLocalDB := tmpFile.Name()
			tmpFile.Close()
			
			if client.Protocol == "sftp" {
				_ = exec.Command("scp", client.Host+":"+filepath.Join(client.RemoteDir, CommitDB), tempLocalDB).Run()
			} else {
				_ = exec.Command("curl", "-s", "-f", "-o", tempLocalDB, client.RemoteDir+"/"+CommitDB).Run()
			}
			targetSnapshot, _ = loadSnapshot(tempLocalDB)
			_ = os.Remove(tempLocalDB)
		}

		updatedTargetSnapshot := make(map[string]FileMeta)
		for k, v := range targetSnapshot {
			updatedTargetSnapshot[k] = v
		}

		var wg sync.WaitGroup
		sem := make(chan struct{}, 10) 
		var mu sync.Mutex

		slots := make([]TaskSlot, 10)
		linesPrinted := 0
		globalTotal := len(plan.Actions)
		globalCompleted := 0
		var globalBytes int64 = 0       
		startTime := time.Now()         

		renderPacmanGrid := func() {
			mu.Lock()
			defer mu.Unlock()

			if linesPrinted > 0 {
				fmt.Printf("\033[%dA", linesPrinted)
			}
			linesPrinted = 0

			for i := 0; i < 10; i++ {
				slot := slots[i]
				if !slot.Active {
					fmt.Print("\033[K\n")
					linesPrinted++
					continue
				}

				pct := 100
				if slot.Total > 0 {
					pct = int((float64(slot.Current) / float64(slot.Total)) * 100)
				}
				if pct > 100 { pct = 100 }

				width := 20
				filled := int((float64(pct) / 100.0) * float64(width))
				barStr := ""
				for b := 0; b < width; b++ {
					if b < filled {
						barStr += "#"
					} else if b == filled {
						if pct == 100 {
							barStr += "#"
						} else if globalCompleted%2 == 0 {
							barStr += "C"
						} else {
							barStr += "c"
						}
					} else {
						if b%2 == 0 { barStr += "o" } else { barStr += " " }
					}
				}

				displayPath := slot.Path
				if len(displayPath) > 22 {
					displayPath = "..." + displayPath[len(displayPath)-19:]
				}

				fmt.Printf("\033[K # Slot %d: [%-22s] [%s] %3d%% (%s)\n", i+1, displayPath, barStr, pct, slot.Action)
				linesPrinted++
			}

			elapsed := time.Since(startTime).Seconds()
			filesPerSec := 0.0
			bytesPerSec := 0.0
			if elapsed > 0 {
				filesPerSec = float64(globalCompleted) / elapsed
				bytesPerSec = float64(globalBytes) / elapsed
			}

			fmt.Printf("\033[K Throughput Performance: %.2f files/s | %s\n", filesPerSec, formatBytesPerSec(bytesPerSec))
			linesPrinted++

			globalPct := int((float64(globalCompleted) / float64(globalTotal)) * 100)
			fmt.Printf("\033[K Total Sync Progress: %d/%d files completed [%d%%]\n", globalCompleted, globalTotal, globalPct)
			linesPrinted++
		}

		renderPacmanGrid()

		for path, action := range plan.Actions {
			if ctx.Err() != nil {
				userInterrupted = true
				break
			}

			lMeta := localLive[path]
			tMeta := targetSnapshot[path]

			wg.Add(1)
			sem <- struct{}{} 

			mu.Lock()
			slotIdx := -1
			for i := 0; i < 10; i++ {
				if !slots[i].Active {
					slotIdx = i
					break
				}
			}
			slots[slotIdx] = TaskSlot{
				Path:   path,
				Action: action,
				Active: true,
			}
			if action == PushToTarget {
				slots[slotIdx].Total = lMeta.Size
			} else if action == PullToLocal {
				slots[slotIdx].Total = tMeta.Size
			} else {
				slots[slotIdx].Total = 1
			}
			mu.Unlock()
			renderPacmanGrid()

			go func(p string, act SyncAction, lm FileMeta, tm FileMeta, targetSlot int) {
				cleanCompletion := true
				defer func() {
					mu.Lock()
					slots[targetSlot].Active = false
					if cleanCompletion {
						globalCompleted++
					}
					mu.Unlock()
					renderPacmanGrid()
					<-sem
					wg.Done()
				}()

				destFullPath := filepath.Join(client.RemoteDir, p)

				switch act {
				case PushToTarget:
					if client.Protocol == "local" {
						localDest := filepath.Join(client.RawPath, p)
						_ = os.MkdirAll(filepath.Dir(localDest), 0755)
						srcFile, err := os.Open(p)
						if err == nil {
							dstFile, err := os.Create(localDest)
							if err == nil {
								buf := make([]byte, 32*1024)
								for {
									if ctx.Err() != nil { cleanCompletion = false; break }
									n, rErr := srcFile.Read(buf)
									if n > 0 {
										_, _ = dstFile.Write(buf[:n])
										mu.Lock()
										slots[targetSlot].Current += int64(n)
										globalBytes += int64(n)
										mu.Unlock()
										renderPacmanGrid()
									}
									if rErr == io.EOF { break }
								}
								dstFile.Close()
							}
							srcFile.Close()
							if !cleanCompletion { _ = os.Remove(localDest) } else { _ = os.Chmod(localDest, lm.Mode) }
						}
					} else if client.Protocol == "sftp" {
						// Setup parent subdirectory structures dynamically over SSH
						_ = exec.Command("ssh", client.Host, "mkdir -p "+filepath.ToSlash(filepath.Dir(destFullPath))).Run()
						cmd := exec.Command("scp", p, client.Host+":"+destFullPath)
						if cmd.Run() == nil {
							mu.Lock(); slots[targetSlot].Current = lm.Size; globalBytes += lm.Size; mu.Unlock()
						} else { cleanCompletion = false }
					} else if client.Protocol == "ftp" {
						cmd := exec.Command("curl", "--ftp-create-dirs", "-T", p, client.RemoteDir+"/"+p)
						if cmd.Run() == nil {
							mu.Lock(); slots[targetSlot].Current = lm.Size; globalBytes += lm.Size; mu.Unlock()
						} else { cleanCompletion = false }
					}

					if cleanCompletion {
						mu.Lock(); updatedTargetSnapshot[p] = lm; mu.Unlock()
					}

				case PullToLocal:
					_ = os.MkdirAll(filepath.Dir(p), 0755)
					if client.Protocol == "local" {
						localSrc := filepath.Join(client.RawPath, p)
						srcFile, err := os.Open(localSrc)
						if err == nil {
							dstFile, err := os.Create(p)
							if err == nil {
								buf := make([]byte, 32*1024)
								for {
									if ctx.Err() != nil { cleanCompletion = false; break }
									n, rErr := srcFile.Read(buf)
									if n > 0 {
										_, _ = dstFile.Write(buf[:n])
										mu.Lock()
										slots[targetSlot].Current += int64(n)
										globalBytes += int64(n)
										mu.Unlock()
										renderPacmanGrid()
									}
									if rErr == io.EOF { break }
								}
								dstFile.Close()
							}
							srcFile.Close()
							if !cleanCompletion { _ = os.Remove(p) } else { _ = os.Chmod(p, tm.Mode) }
						}
					} else if client.Protocol == "sftp" {
						cmd := exec.Command("scp", client.Host+":"+destFullPath, p)
						if cmd.Run() == nil {
							_ = os.Chmod(p, tm.Mode)
							mu.Lock(); slots[targetSlot].Current = tm.Size; globalBytes += tm.Size; mu.Unlock()
						} else { cleanCompletion = false }
					} else if client.Protocol == "ftp" {
						cmd := exec.Command("curl", "-o", p, client.RemoteDir+"/"+p)
						if cmd.Run() == nil {
							_ = os.Chmod(p, tm.Mode)
							mu.Lock(); slots[targetSlot].Current = tm.Size; globalBytes += tm.Size; mu.Unlock()
						} else { cleanCompletion = false }
					}

					if cleanCompletion {
						mu.Lock()
						localLive[p] = tm
						updatedTargetSnapshot[p] = tm
						mu.Unlock()
					}

				case DeleteTarget:
					if ctx.Err() == nil {
						if client.Protocol == "local" {
							_ = os.Remove(filepath.Join(client.RawPath, p))
							pruneEmptyDirs(client.RawPath, filepath.Dir(p))
						} else if client.Protocol == "sftp" {
							_ = exec.Command("ssh", client.Host, "rm -f "+destFullPath).Run()
						} else if client.Protocol == "ftp" {
							_ = exec.Command("curl", "-Q", "DELE "+p, client.RemoteDir).Run()
						}
						mu.Lock()
						slots[targetSlot].Current = 1
						delete(updatedTargetSnapshot, p)
						delete(localLive, p)
						mu.Unlock()
					} else {
						cleanCompletion = false
					}

				case DeleteLocal:
					if ctx.Err() == nil {
						_ = os.Remove(p)
						pruneEmptyDirs(".", filepath.Dir(p))
						mu.Lock()
						slots[targetSlot].Current = 1
						delete(localLive, p)
						delete(updatedTargetSnapshot, p)
						mu.Unlock()
					} else {
						cleanCompletion = false
					}
				}
			}(path, action, lMeta, tMeta, slotIdx)
		}

		wg.Wait()
		linesPrinted = 0 
		fmt.Println()    

		if !userInterrupted {
			// Serialize and commit database snapshots back down into target nodes safely
			if client.Protocol == "local" {
				_ = os.MkdirAll(filepath.Dir(targetCommitDB), 0755)
				_ = saveSnapshot(targetCommitDB, updatedTargetSnapshot)
			} else {
				tmpFile, _ := os.CreateTemp("", "nxsync-upload-*.bin")
				tempLocalDB := tmpFile.Name()
				tmpFile.Close()
				
				_ = saveSnapshot(tempLocalDB, updatedTargetSnapshot)
				if client.Protocol == "sftp" {
					_ = exec.Command("ssh", client.Host, "mkdir -p "+filepath.ToSlash(filepath.Join(client.RemoteDir, ".nxsync"))).Run()
					_ = exec.Command("scp", tempLocalDB, client.Host+":"+filepath.Join(client.RemoteDir, CommitDB)).Run()
				} else if client.Protocol == "ftp" {
					_ = exec.Command("curl", "--ftp-create-dirs", "-T", tempLocalDB, client.RemoteDir+"/"+CommitDB).Run()
				}
				_ = os.Remove(tempLocalDB)
			}
			fmt.Printf("[+] Target [%s] unified successfully.\n", name)
		}
	}

	_ = saveSnapshot(CommitDB, localLive)

	if userInterrupted {
		fmt.Fprintln(os.Stderr, "\n[!] Operation aborted mid-execution by user. Safe states maintained.")
		os.Exit(130)
	} else {
		fmt.Println("[*] Multi-source synchronization loop successfully completed.")
	}
}