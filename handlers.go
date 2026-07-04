package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

	fmt.Println("\nRecording Mutations to Commit Ledger:")
	for _, change := range changesMap {
		fmt.Printf("   [%s] %s\n", change.Type, change.Path)
	}

	_ = saveSnapshot(CommitDB, currentFilesState)
	fmt.Println("\n[*] State index synchronization committed cleanly to commit.bin.")
}

// NEW: Discards all pending local adjustments and forces checkout of remote target states completely
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

	targetCommitDB := filepath.Join(targetDest, CommitDB)
	if _, err := os.Stat(targetCommitDB); os.IsNotExist(err) {
		fmt.Printf("Fatal: target configuration location [%s] is not initialized with a commit.bin file.\n", targetName)
		os.Exit(1)
	}

	targetSnapshot, _ := loadSnapshot(targetCommitDB)
	localLive, _ := calculateDeltaMatrix(false)

	restoreActions := make(map[string]SyncAction)
	allPaths := make(map[string]bool)
	for p := range localLive { allPaths[p] = true }
	for p := range targetSnapshot { allPaths[p] = true }

	for path := range allPaths {
		if path == StateDir || strings.HasPrefix(path, StateDir+"/") || strings.HasPrefix(path, StateDir+string(filepath.Separator)) || strings.Contains(path, "targets.json") || strings.Contains(path, "commit.bin") {
			continue
		}

		lMeta, lHasLive := localLive[path]
		tMeta, tHasSnap := targetSnapshot[path]

		if tHasSnap && !lHasLive {
			restoreActions[path] = PullToLocal // Missing locally -> Pull from target
		} else if !tHasSnap && lHasLive {
			restoreActions[path] = DeleteLocal // Extra local file -> Destroy it
		} else if tHasSnap && lHasLive {
			if lMeta.Size != tMeta.Size || lMeta.Mode != tMeta.Mode || lMeta.Timestamp != tMeta.Timestamp {
				restoreActions[path] = PullToLocal // Content mismatch -> Force overwrite from target
			}
		}
	}

	if len(restoreActions) == 0 {
		fmt.Printf("[*] Local environment already matches target layout [%s] perfectly.\n", targetName)
		return
	}

	fmt.Printf("\n[!] Compiled Destructive Restore Plan using Target Environment [%s]:\n", targetName)
	for path, action := range restoreActions {
		fmt.Printf("   [%s] %s\n", action, path)
	}
	fmt.Print("\n[WARNING] This operation is destructive. Local changes will be lost.\nPress [ENTER] to execute restore checkout, or [CTRL+C] to abort...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')

	for path, action := range restoreActions {
		targetFullPath := filepath.Join(targetDest, path)

		switch action {
		case PullToLocal:
			_ = os.MkdirAll(filepath.Dir(path), 0755)
			if err := copyFile(targetFullPath, path); err == nil {
				_ = os.Chmod(path, targetSnapshot[path].Mode)
				localLive[path] = targetSnapshot[path]
			}
		case DeleteLocal:
			_ = os.Remove(path)
			pruneEmptyDirs(".", filepath.Dir(path))
			delete(localLive, path)
		}
	}

	// Force local ledger cache state to exactly reflect target binary indices
	_ = saveSnapshot(CommitDB, localLive)
	fmt.Println("[*] Restore configuration checkout operation completed successfully.")
}

func handleTarget(args []string) {
	if len(args) == 0 || args[0] == "list" {
		targets, _ := loadTargets(TargetsConf)
		if len(targets) == 0 {
			fmt.Println("No targets registered yet. Run: 'nxsync target add <name> <path>'")
			return
		}
		fmt.Println("Registered Sync Destinations:")
		for name, path := range targets {
			fmt.Printf("   %s\t->  %s\n", name, path)
		}
		return
	}

	if args[0] == "add" && len(args) >= 3 {
		targets, _ := loadTargets(TargetsConf)
		name := args[1]
		path := args[2]
		targets[name] = path
		_ = saveTargets(TargetsConf, targets)
		fmt.Printf("[+] Registered destination node '%s' pointing to: %s\n", name, path)
		return
	}
	fmt.Println("Invalid syntax. Usage: 'target add <name> <path>' or 'target list'")
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
	hasAnyActions := false

	for name, targetDest := range targetsToSync {
		targetCommitDB := filepath.Join(targetDest, CommitDB)
		
		_, err := os.Stat(targetCommitDB)
		targetInitialized := err == nil

		actions := make(map[string]SyncAction)

		if !targetInitialized {
			for path := range localLive {
				if strings.Contains(path, "targets.json") || strings.Contains(path, "commit.bin") || strings.Contains(path, "preview") {
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

		targetSnapshot, _ := loadSnapshot(targetCommitDB)

		allPaths := make(map[string]bool)
		for p := range localLive { allPaths[p] = true }
		for p := range localSnapshot { allPaths[p] = true }
		for p := range targetSnapshot { allPaths[p] = true }

		for path := range allPaths {
			if strings.Contains(path, "targets.json") || strings.Contains(path, "commit.bin") || strings.Contains(path, "preview") {
				continue
			}

			lMeta, lHasLive := localLive[path]
			_, lHasSnap := localSnapshot[path]
			tMeta, tHasSnap := targetSnapshot[path]

			if lHasLive && tHasSnap {
				if lMeta.Timestamp == tMeta.Timestamp && lMeta.Size == tMeta.Size {
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

	fmt.Println("\n[*] Global Multi-Target Sync Plan Compiled:")
	for name, plan := range allPlans {
		fmt.Printf("\nTarget Location [%s] -> %s:\n", name, plan.Dest)
		for path, action := range plan.Actions {
			fmt.Printf("   [%s] %s\n", action, path)
		}
	}
	fmt.Print("\nPress [ENTER] to execute all planned changes across workspace boundaries, or [CTRL+C] to abort...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')

	for name, plan := range allPlans {
		fmt.Printf("[*] Synchronizing target pipeline context [%s]...\n", name)
		targetCommitDB := filepath.Join(plan.Dest, CommitDB)
		targetSnapshot, _ := loadSnapshot(targetCommitDB)

		updatedTargetSnapshot := make(map[string]FileMeta)
		for k, v := range targetSnapshot {
			updatedTargetSnapshot[k] = v
		}

		for path, action := range plan.Actions {
			destFullPath := filepath.Join(plan.Dest, path)

			switch action {
			case PushToTarget:
				_ = os.MkdirAll(filepath.Dir(destFullPath), 0755)
				if err := copyFile(path, destFullPath); err == nil {
					_ = os.Chmod(destFullPath, localLive[path].Mode)
					updatedTargetSnapshot[path] = localLive[path] 
				}
			case PullToLocal:
				_ = os.MkdirAll(filepath.Dir(path), 0755)
				if err := copyFile(destFullPath, path); err == nil {
					_ = os.Chmod(path, targetSnapshot[path].Mode)
					localLive[path] = targetSnapshot[path] 
					updatedTargetSnapshot[path] = targetSnapshot[path] 
				}
			case DeleteTarget:
				_ = os.Remove(destFullPath)
				pruneEmptyDirs(plan.Dest, filepath.Dir(path))
				delete(updatedTargetSnapshot, path)
				delete(localLive, path)
			case DeleteLocal:
				_ = os.Remove(path)
				pruneEmptyDirs(".", filepath.Dir(path))
				delete(localLive, path)
				delete(updatedTargetSnapshot, path)
			}
		}

		_ = os.MkdirAll(filepath.Dir(targetCommitDB), 0755)
		_ = saveSnapshot(targetCommitDB, updatedTargetSnapshot)
		fmt.Printf("[+] Target [%s] unified successfully.\n", name)
	}

	_ = saveSnapshot(CommitDB, localLive)
	fmt.Println("[*] Multi-source synchronization loop successfully completed.")
}