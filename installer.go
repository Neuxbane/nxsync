package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ensureInstalled() {
	exePath, err := os.Executable()
	if err != nil {
		return 
	}

	cwd, _ := os.Getwd()
	absExePath, _ := filepath.Abs(exePath)
	
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	binDir := filepath.Join(home, ".bin")
	targetPath := filepath.Join(binDir, "nxsync")

	// Detect if executed from current working directory or explicitly via relative path strings
	isLocalExecution := absExePath == filepath.Join(cwd, filepath.Base(absExePath)) || strings.HasPrefix(os.Args[0], "./")

	if isLocalExecution && absExePath != targetPath {
		fmt.Printf("[*] nxsync: Local path execution detected. Overwriting binary in %s...\n", targetPath)
		
		if err := os.MkdirAll(binDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "nxsync: installation error: %v\n", err)
			os.Exit(1)
		}

		if err := copyFile(absExePath, targetPath); err != nil {
			fmt.Fprintf(os.Stderr, "nxsync: installation error: %v\n", err)
			os.Exit(1)
		}

		if err := os.Chmod(targetPath, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "nxsync: installation error: %v\n", err)
			os.Exit(1)
		}

		shell := os.Getenv("SHELL")
		var rcFile string
		if strings.Contains(shell, "zsh") {
			rcFile = filepath.Join(home, ".zshrc")
		} else if strings.Contains(shell, "bash") {
			rcFile = filepath.Join(home, ".bashrc")
		} else {
			rcFile = filepath.Join(home, ".profile")
		}

		pathExport := fmt.Sprintf("\nexport PATH=\"$HOME/.bin:$PATH\"\n")
		
		content, err := os.ReadFile(rcFile)
		if err == nil {
			if !strings.Contains(string(content), "$HOME/.bin") {
				f, err := os.OpenFile(rcFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
				if err == nil {
					_, _ = f.WriteString(pathExport)
					f.Close()
					fmt.Println("nxsync: added ~/.bin to PATH in", rcFile)
				}
			}
		}
		
		fmt.Println("\n[+] Success! Global binary overwritten and updated successfully.")
		os.Exit(0) // Exit cleanly immediately without evaluating downstream router commands
	}
}