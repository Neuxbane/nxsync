package main

import (
	"fmt"
	"os"
)

func main() {
	ensureInstalled()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "-h", "--help", "help":
		printUsage()
		os.Exit(0)
	case "-v", "--version", "version":
		printVersion()
		os.Exit(0)
	case "init":
		handleInit()
	case "commit":
		assertInWorkspace()
		handleCommit()
	case "restore":
		assertInWorkspace()
		handleRestore(os.Args[2:])
	case "target":
		assertInWorkspace()
		handleTarget(os.Args[2:])
	case "preview":
		assertInWorkspace()
		handlePreview()
	case "sync":
		assertInWorkspace()
		handleSync(os.Args[2:], true, false)
	case "daemon":
		assertInWorkspace()
		handleDaemon(len(os.Args) > 2 && os.Args[2] == "sync")
	default:
		fmt.Printf("nxsync: '%s' is not an nxsync command. See 'nxsync --help'.\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("usage: nxsync [-v | --version] [-h | --help] <command> [<args>]\n")
	fmt.Println("These are common nxsync commands used in various situations:\n")
	fmt.Println("start a tracking workspace")
	fmt.Println("   init                 Initialize an empty nxsync workspace layout")
	fmt.Println("   commit               Scan and save current additions/deletions into binary ledger\n")
	fmt.Println("manage workspace environments")
	fmt.Println("   restore <target>     Discard current changes and force reset to target state\n")
	fmt.Println("manage sync targets")
	fmt.Println("   target add <n> <p>    Register a target destination node path")
	fmt.Println("   target list           List all registered target destination records\n")
	fmt.Println("examine state (local view only)")
	fmt.Println("   preview               Mount virtual FUSE overlay checking local configurations\n")
	fmt.Println("synchronize state")
	fmt.Println("   sync                 Validate modifications and replicate across ALL target ledgers")
	fmt.Println("   sync all             Validate modifications and replicate across ALL target ledgers")
	fmt.Println("   sync <target-name>    Validate modifications and execute push sync routines to target\n")
	fmt.Println("background automation")
	fmt.Println("   daemon                Start a background watcher that records changes in real-time")
	fmt.Println("   daemon sync           Trigger manual synchronization while daemon is running")
}

func printVersion() {
	fmt.Printf("nxsync version %s\n", Version)
}