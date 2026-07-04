package main

import (
	"context"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type TreePath struct {
	Name     string
	IsDir    bool
	Children map[string]*TreePath
}

type PreviewDir struct {
	fs.Inode
}

type PreviewNode struct {
	fs.Inode
	Children map[string]*TreePath
}

func (n *PreviewNode) OnAdd(ctx context.Context) {
	buildFuseTree(ctx, &n.Inode, n.Children)
}

func buildFuseTree(ctx context.Context, parent *fs.Inode, treeChildren map[string]*TreePath) {
	for name, node := range treeChildren {
		if node.IsDir {
			subDir := &PreviewDir{}
			stable := fs.StableAttr{Mode: fuse.S_IFDIR | 0755}
			childInode := parent.NewInode(ctx, subDir, stable)
			parent.AddChild(name, childInode, true)
			
			buildFuseTree(ctx, childInode, node.Children)
		} else {
			file := &fs.MemRegularFile{
				Data: []byte(""),
			}
			stable := fs.StableAttr{Mode: fuse.S_IFREG | 0644}
			childInode := parent.NewInode(ctx, file, stable)
			parent.AddChild(name, childInode, true)
		}
	}
}