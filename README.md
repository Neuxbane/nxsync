# nxsync

`nxsync` is a lightweight, high-performance, bidirectional (two-way) state synchronization and replication engine written in Go. Designed to bypass the fragility of standard file modification timestamps (`ModTime`), it tracks modifications using a **purely content-driven and permission-driven** architecture stored in an obfuscated custom binary ledger. 

Featuring automatic self-installation, robust global multi-target coordination, a destructive state recovery engine, and an interactive virtual FUSE layout system, `nxsync` provides flawless peer-to-peer file synchronization across directories and drives.

---

## 🚀 Key Features

* **Purely Content & Permission Driven:** Timestamps are detached from standard OS modification triggers. A file's commit version changes *only* if its hardware byte-size or permission mask (`os.FileMode`) is modified.
* **Obfuscated Binary Ledger Backend:** Your local directory structures are packed into a custom `.bin` format using a space-efficient binary layout. Path mappings are encoded using fixed 16-byte MD5 hashes to optimize seek speed and abstract raw metadata.
* **True Two-Way Synchronization:** Implements an advanced state-reconciliation matrix comparing live layouts against remote target ledgers. It safely distinguishes between manual deletions vs. newly added branches across multi-device endpoints.
* **Target Configuration Quarantine:** Built-in hardcoded structural isolation blacklists tracking elements like `targets.json` and `commit.bin` to eliminate sync loops and protect target routing configurations.
* **Instant Target Initialization:** Syncing to an empty folder automatically hooks, structures, and initializes the target location with tracking components (`safe.conf`, `ignore.conf`) and a baseline push allocation, entirely transparently.
* **FUSE Overlay Previews:** Leverage high-level Linux FUSE wrappers to temporarily mount an in-memory virtual preview directory showing your structural configuration boundaries and rules mappings natively without modifying disk data.
* **Destructive Environment Restores:** Execute force checkouts to match specific remote states instantly, wiping out un-tracked changes and downloading identical assets using target database blueprints.

---

## 🛠️ Installation & Setup

### Prerequisites
Make sure you have Go installed on your system along with the necessary FUSE file-system dependencies (`libfuse3` / `fusermount3`).

```bash
# Arch Linux
sudo pacman -S fuse3

# Ubuntu/Debian
sudo apt install fuse3 libfuse3-dev

```

### Building from Source

Clone your project directory and execute a standard compilation step:

```bash
go clean
go build -o nxsync

```

### Self-Installation Mechanism

`nxsync` features an automated onboarding guardrail. Running the newly compiled binary using its local/relative file path triggers an immediate global installation sequence:

```bash
./nxsync

```

> **What happens here:** The binary detects relative execution paths, copies/overwrites itself into `$HOME/.bin/nxsync`, appends your active shell environment variables (`.bashrc` / `.zshrc`) to expose the PATH globally, and drops the execution context safely. Relative paths are immediately blocked to protect systemic state hooks.

---

## 📋 Command Matrix Reference

Once installed globally, utilize the master binary directly from any terminal prompt:

| Command | Arguments | Description |
| --- | --- | --- |
| `init` | None | Spawns tracking workspace directories (`.nxsync/`), and default tracking configs. |
| `commit` | None | Scans workspace bounds and records current sizes/modes to the custom binary database. |
| `target add` | `<name> <path>` | Registers a target destination folder tracking hook profile mapping. |
| `target list` | None | Lists all saved targets along with their assigned path configurations. |
| `preview` | None | Mounts an interactive virtual FUSE mirror within `.nxsync/preview` for system analysis. |
| `sync` | `[all | name]` | Auto-commits local state and applies a clean bidirectional synchronization across targeted nodes. |
| `restore` | `<target-name>` | **Destructive.** Discards local mutations entirely to match the chosen remote target layout. |

---

## ⚙️ Workspaces Configuration

Upon calling `nxsync init`, an environment container workspace is spun up locally containing the following foundational items:

### 1. `safe.conf`

Defines the file or folder directories the crawler is allowed to index. Defaults to tracking the active project root directory via standard absolute path markers:

```text
# Root directory boundaries to track. '.' maps to workspace root.
.

```

### 2. `ignore.conf`

Contains simple line-by-line pattern exclusions. Any assets pointing into directories mapped here are ignored completely by the execution processing chains:

```text
# Explicit patterns to exclude from sync routines
.git/
*.log
tmp/

```

---

## 🧠 Behind the Scenes: The Sync Matrix Protocol

When running a `sync` operation, `nxsync` avoids scanning foreign file-systems directly. Instead, it references the target's `.nxsync/commit.bin` directly as the absolute source of truth. The engine parses the global states into a unified matrix map and evaluates alignment using the following truth table rules logic:

* **Matching Timestamps & Sizes:** The file is flagged as completely unmodified and **skipped** immediately, completely neutralizing network and disk copy overhead.
* **Local Modification / Addition:** If the local asset's generic commit timestamp is newer than the target's metadata, a **`[PUSH]`** action is registered.
* **Remote Modification / Addition:** If the target ledger features a newer generic execution timestamp than yours, a backward-flowing **`[PULL]`** action is registered.
* **Local Deletion:** If a file is absent from your disk but was previously recorded inside the snapshot ledger, the system understands it was deleted manually, registering a **`[DEL_TARGET]`** modification loop to erase it remotely.
* **Remote Deletion:** If an entry exists inside the host environment but vanishes from the target's snapshot records, a **`[DEL_LOCAL]`** action is executed to mirror that extraction cleanly.

---

## ⚖️ License

Distributed under the MIT License. See `LICENSE` for more details.
