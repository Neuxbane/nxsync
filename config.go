package main

import (
	"os"
)

const Version = "1.0.0"

type ChangeType string

const (
	Modified  ChangeType = "M"
	Deleted   ChangeType = "D"
	Added     ChangeType = "C"
	Unchanged ChangeType = " "
	Ignored   ChangeType = "I"
)

type Change struct {
	Path string
	Type ChangeType
}

type FileMeta struct {
	Size      int64       `json:"size"`
	Timestamp int64       `json:"timestamp"` 
	Mode      os.FileMode `json:"mode"`
	IsDeleted bool        `json:"isDeleted"`
}

const (
	StateDir    = ".nxsync"
	CommitDB    = ".nxsync/commit.bin" // UPGRADED: Swapped JSON ledger for fixed binary storage layout
	SafeConf    = ".nxsync/safe.conf"
	IgnoreConf  = ".nxsync/ignore.conf"
	TargetsConf = ".nxsync/targets.json"
)