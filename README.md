# Local File Organizer

**Tidy your Downloads, Desktop and Documents without doing it by hand, and without trusting a tool to delete things behind your back.**

Local File Organizer scans your folders and finds:
- duplicates
- empty files and folders
- leftover partial downloads
- forgotten old downloads

It also sorts loose files into folders using simple rules. It shows you everything it wants to do first. Nothing moves or gets deleted until you approve it, and every batch can be undone.

It's free and runs entirely on your computer, with no cloud, no account and no telemetry. It works on Windows, macOS and Linux.

> **About this project.** This is a personal tooling project for lazy people like me who'd rather click "approve" than sort 9,000 files by hand. It was designed and built with [Claude](https://claude.ai), Anthropic's AI assistant, using Claude Code. I reviewed, tested and published it. I didn't write this README either; Claude did that too. That's how lazy I am. I just want tools that exist for exactly this kind of reason. It's built to a professional standard (tested, documented, and careful with your files), but it's a hobby project, not a commercial product. See [Disclaimer](#disclaimer).

---

## Contents

- [Why it's safe to use](#why-its-safe-to-use)
- [Install](#install)
- [Quick start](#quick-start)
- [The review page](#the-review-page)
- [What it finds](#what-it-finds)
- [Sorting with rules](#sorting-with-rules)
- [Settings](#settings)
- [Undo, the holding area and the trash](#undo-the-holding-area-and-the-trash)
- [Command line reference](#command-line-reference)
- [FAQ](#faq)
- [Known limitations](#known-limitations)
- [Building from source](#building-from-source)
- [How it works](#how-it-works)
- [Credits](#credits)
- [Disclaimer](#disclaimer)
- [License](#license)

---

## Why it's safe to use

Deleting the wrong file is the main risk with any cleanup tool, so this one is built around avoiding it:

- **Nothing happens without your approval.** Scanning and planning only read. Changes happen only after you press *Approve*, or type `yes` in the terminal.
- **Deletes aren't really deletes, at first.** Approved deletes go to a **holding area** for 30 days, where one click restores them exactly. After that they go to your normal **Recycle Bin or Trash**, where you can still restore them.
- **Every file is re-checked right before it's touched.** If a file changed since the scan, it's skipped.
- **It never deletes your last copy.** Before removing a duplicate, it checks that the copy you're keeping still exists. It also confirms both files still have identical content, using a full BLAKE3 hash.
- **It leaves alone things it shouldn't touch:**
  - **Project folders** are treated as one unit and never reorganised. A project folder is any folder containing `.git`, `package.json`, `go.mod`, `Cargo.toml`, `.sln`, `.claude`, a Rojo `default.project.json`, or similar.
  - **System and app folders** are always protected: Windows, Program Files, AppData, `~/Library`, `/usr` and so on.
  - **Cloud/online-only files** from OneDrive or iCloud are never opened, so the tool never triggers a download.
  - **Symlinks and junctions** are never followed.
  - **Subfolders you made yourself** inside Downloads are never sorted or flagged as "old downloads". Only loose files are.
- **Every action is journaled before it happens.** Even a crash or power cut mid-batch can be recovered and undone.
- **The review page is private to your computer.** It listens on `127.0.0.1` only and needs a one-time link printed in your terminal.

---

## Install

### Option 1: download a ready-made program (easiest)

Go to the [**Releases**](https://github.com/phillip8232/local-file-organizer/releases) page and download the file for your system:

| System | File |
|---|---|
| Windows (most PCs) | `organizer-windows-amd64.exe` |
| Windows on ARM (Snapdragon laptops, Surface Pro X) | `organizer-windows-arm64.exe` |
| macOS on Apple Silicon (M1 and later) | `organizer-darwin-arm64` |
| macOS on Intel | `organizer-darwin-amd64` |
| Linux x86-64 | `organizer-linux-amd64` |
| Linux ARM64 (64-bit Raspberry Pi OS) | `organizer-linux-arm64` |

Rename it to `organizer` (`organizer.exe` on Windows) and put it somewhere handy.

**Windows:** the program isn't code-signed (this is a free hobby project), so SmartScreen may say *"Windows protected your PC"*. Click **More info → Run anyway**. If you'd rather not, use Option 2 and build it yourself.

**macOS:** the first time, macOS will refuse to open an unsigned download. Run this once in Terminal, from the folder where the file is:

```bash
chmod +x organizer-darwin-arm64 && xattr -d com.apple.quarantine organizer-darwin-arm64
```

**Linux:** make it executable with `chmod +x organizer-linux-amd64`.

### Option 2: install with Go

If you have [Go](https://go.dev/dl/) 1.27 or newer:

```bash
go install github.com/phillip8232/local-file-organizer/cmd/organizer@latest
```

That puts `organizer` in your Go bin folder (`~/go/bin`, or `%USERPROFILE%\go\bin` on Windows).

---

## Quick start

Open a terminal (PowerShell on Windows, Terminal on macOS/Linux) in the folder where you put the program and run:

```bash
organizer ui
```

On Windows, if you're in the same folder, type `.\organizer.exe ui`.

Your browser opens the review page. Then:

1. Click **Scan now**. It reads your Downloads, Desktop and Documents folders. Only the file list is read; contents are read only to compare possible duplicates. A rescan takes about a second.
2. On the **Clean up** tab, click **Find things to clean up**. Look through the sections, untick anything you want to keep, and choose which copy of each duplicate to keep.
3. Click **Review and approve…**, read the summary, and confirm.
4. Changed your mind? Open **History** and click **Undo** on that batch.

To stop the program, press **Ctrl+C** in the terminal.

---

## The review page

| Tab | What it's for |
|---|---|
| **Clean up** | Duplicates, empty folders and files, junk, old downloads and large files, grouped and ticked by default where it's safe. |
| **Sort** | A preview of where your rules would move loose files. Untick anything, then approve. |
| **History** | Every batch you've approved, with **Undo** and a per-file log. Also shows what's in the holding area and lets you send old items to the trash now. |
| **Rules** | Edit your sort rules in the browser. They're checked when you save. |

**Show** next to any file opens its folder in Explorer or Finder with the file selected. It never opens or runs the file itself.

---

## What it finds

| Category | How it decides | Ticked by default? |
|---|---|---|
| **Duplicates** | Files with identical content: same size, then matching samples from start, middle and end, then a full BLAKE3 hash. Hard links count as one file. | Yes. One copy per group is always kept; you pick which. |
| **Empty folders** | Contain nothing except empty folders or OS clutter like `Thumbs.db`, `desktop.ini` or `.DS_Store`. | Yes |
| **Empty files** | 0 bytes. | Yes |
| **Junk** | Partial downloads (`.crdownload`, `.part`, `.partial`), Office lock files (`~$…`) and `.tmp` files, all at least a day old, so a download in progress is never touched. | Yes |
| **Old downloads** | Loose files in Downloads not created or modified in 90 days. Configurable. | **No**: tick what you want gone |
| **Largest files** | Your 50 biggest files, for review. | **No** |

**Which duplicate is kept?** By default the tool prefers:
1. a copy outside Downloads and Desktop,
2. then one that doesn't look like a copy (`name (1).jpg`, `name - Copy.jpg`),
3. then the oldest.

You can override this per group on the review page. You can also change the default to `oldest` or `newest` in settings.

---

## Sorting with rules

Rules live in `rules.yaml`. Edit them in the **Rules** tab, or open the file directly; run `organizer where` to find it. Rules are checked top to bottom, and the first rule that matches a file wins.

The defaults sort loose files in Downloads into `Images/<year>`, `Videos`, `Audio`, `Documents/<year>`, `Archives`, `Installers` and `3D`. Screenshots in Downloads and on the Desktop go to `Screenshots/<year>-<month>`.

```yaml
version: 1
rules:
  - name: Screenshots
    in: [Downloads, Desktop]
    match: { type: [image], name: "Screenshot*" }
    move_to: Screenshots/{year}-{month}

  - name: Big videos to my Videos folder
    in: [Downloads]
    match: { type: [video], min_size: 500MB }
    move_to: "@Videos/From Downloads"

  - name: Old installers
    in: [Downloads]
    match: { type: [installer], older_than_days: 30 }
    move_to: Installers
```

| Field | Meaning |
|---|---|
| `name` | A label shown in the preview. |
| `in` | Where the rule looks: `Downloads`, `Desktop`, `Documents`, or a folder path. Leave it out to use every scanned folder. |
| `recursive` | `false` (default) sorts only files sitting directly in the folder. `true` also looks in subfolders. |
| `match.type` | Any of `image video audio document spreadsheet presentation archive installer code font 3d ebook`. |
| `match.ext` | Extensions, e.g. `[psd, ai]`. |
| `match.name` | A filename pattern, e.g. `"Invoice*"`. Not case-sensitive. |
| `match.older_than_days` | Only files not modified for this many days. |
| `match.min_size` / `match.max_size` | E.g. `10MB`, `2GB`. |
| `move_to` | Where matching files go. See below. Quote values starting with `@` or `{`. |

How `move_to` is read:
- **A relative path** like `Images/{year}` is created inside the folder the file was found in.
- **`@Pictures/...`** means your system Pictures folder. This works for `@Documents`, `@Videos`, `@Music`, `@Downloads` and `@Desktop` too.
- **`~/...` or a full path** goes exactly there.
- **Tokens:** `{year}`, `{month}` and `{day}` come from the file's modified date. `{type}` and `{ext}` come from the file itself.

A rule needs at least one `match` condition, so a typo can't turn into "move everything". Moves never overwrite: if a name is taken, the file becomes `name (2).ext`.

---

## Settings

Run `organizer where` to see where everything lives. The default folder is:

| System | Data folder |
|---|---|
| Windows | `%LOCALAPPDATA%\organizer` |
| macOS | `~/Library/Application Support/organizer` |
| Linux | `~/.local/share/organizer` |

`config.yaml` in that folder is created on first run with these defaults:

```yaml
roots: [Downloads, Desktop, Documents]   # folders to organise; names or paths
protected: []                            # extra folders to never touch
ignore_dirs: [.git, node_modules, ...]   # folder names never scanned
old_downloads_days: 90                   # age for "old downloads"
holding_days: 30                         # days deleted items stay restorable
io_workers: 4                            # parallel reads while hashing; use 1 for a spinning HDD
large_top_n: 50                          # how many large files to list
min_duplicate_size: 1024                 # ignore duplicates smaller than this (bytes)
keep: smart                              # which duplicate to keep: smart, oldest, newest
```

Downloads, Desktop and Documents follow your system's real locations, including when Windows has moved them into OneDrive.

Use `--data <folder>` before any command to keep a separate set of settings, for example to test on a scratch folder:

```bash
organizer --data ./test-data scan
```

---

## Undo, the holding area and the trash

- When you approve deletes, the files are **moved**, not deleted, into the holding area inside the data folder. The move is instant and costs no extra disk space.
- **Undo** in History puts everything from that batch back exactly where it was. If something new now has the same name, the restored file comes back as `name (2)`.
- After `holding_days` (30 by default), items go to your **Recycle Bin or Trash**. This happens the next time you open the review page, or when you run `organizer purge`. They're put back at their original location first, so your Recycle Bin's own **Restore** returns them to the right folder.
- Want the space back sooner? History → **Send now**, or `organizer purge --days 0`.
- `organizer purge --permanent` deletes held items **forever**, skipping the trash. It asks you to type `delete forever` first, and it's only available on the command line.

---

## Command line reference

Everything the review page does is also available in the terminal:

```text
organizer ui [--port N] [--no-browser]   open the review page (recommended)
organizer scan                           index the configured folders
organizer report [--limit N] [--json]    show what the analyzers found
organizer plan cleanup|sort              build a plan to review (changes nothing)
organizer apply <plan> [--include c,c] [--yes]
                                         carry out a plan's ticked items
organizer history                        list past batches
organizer show <batch>                   list a batch's steps
organizer undo <batch>                   reverse a batch
organizer purge [--days N] [--permanent] empty old items from the holding area
organizer rules                          check rules.yaml
organizer where                          show where settings and data live
```

Typical terminal session:

```bash
organizer scan
organizer report
organizer plan cleanup                  # prints "Plan 3 saved"
organizer apply 3                       # asks you to type yes
                                        # (or: apply 3 --include old-download
                                        #  to delete the old downloads too)
organizer history                       # shows "#1 ... done"
organizer undo 1                        # put it all back
```

---

## FAQ

**Does it upload anything?**
No. It makes no network requests at all. The review page is served by the program itself, on your own computer only.

**Will it mess up my code projects or game projects?**
No. Any folder containing a project marker (`.git`, `package.json`, `go.mod`, `.sln`, a Rojo `default.project.json`, `.claude` and others) is skipped as a whole. The report lists which folders it left alone.

**My Documents folder is in OneDrive. Is that OK?**
Yes. Files that are online-only aren't opened, so nothing gets downloaded. They're never moved or deleted either. Files that are fully local are treated like any other file. Keep in mind that deleting a synced file also removes it from OneDrive, the same as deleting it in Explorer would.

**It skipped some files. Why?**
Usually because the file changed after you scanned. The result window and History → Details give the reason for each skip. Scan again and rebuild the plan.

**Can I add other folders, like a second drive?**
Yes: add them to `roots` in `config.yaml`. For now, rules can only move files within the same drive.

**How fast is it?**
On a normal SSD, around 9,000 files (165 GB) index in about 2 seconds, and a rescan takes about 1 second. Only files that could be duplicates are hashed, and hashes are cached between scans.

---

## Known limitations

- **macOS** builds compile but haven't been tested on a real Mac yet. Trashing goes through Finder, so macOS will ask once for permission to control Finder. Reports from Mac users are very welcome.
- **Moving files between drives** isn't supported yet; those moves are refused with a clear message.
- **Watching folders** to sort new files automatically isn't built yet.
- **Similar (not identical) images** aren't detected; only exact duplicates are.
- The binaries aren't code-signed (see [Install](#install)).

---

## Building from source

You need [Go](https://go.dev/dl/) 1.27 or newer. No C compiler is needed.

```bash
git clone https://github.com/phillip8232/local-file-organizer
cd local-file-organizer
go test ./...
go build -o organizer ./cmd/organizer
```

To cross-compile, set `GOOS`/`GOARCH`. Every target builds from any OS:

```bash
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o dist/organizer-darwin-arm64 ./cmd/organizer
```

The trash test puts a real file in your OS trash, so it only runs when you set `ORGANIZER_TEST_TRASH=1`.

---

## How it works

```text
Walker ──▶ Index (SQLite) ──▶ Analyzers ──▶ Plan ──▶ Review ──▶ Executor
fast,       paths, sizes,     duplicates,   moves &   you tick   verify, journal,
parallel,   dates, hashes     empty, junk,  deletes   & approve  hold / move, undo
read-only   (cached)          old, rules
```

| Part | Library |
|---|---|
| Directory walking | [charlievieth/fastwalk](https://github.com/charlievieth/fastwalk) |
| Index and journal | [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) (pure Go, no CGO) |
| Hashing | [zeebo/xxh3](https://github.com/zeebo/xxh3) for samples, [zeebo/blake3](https://github.com/zeebo/blake3) for full hashes |
| Rules | [gopkg.in/yaml.v3](https://github.com/go-yaml/yaml) |
| OS calls | [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) |

Trash support is built in, with no extra dependency:
- **Windows:** `SHFileOperationW` with undo enabled.
- **macOS:** Finder.
- **Linux:** the FreeDesktop.org Trash spec.

The review page is plain HTML, CSS and JavaScript embedded in the binary.

---

## Credits

Designed and written with **[Claude](https://claude.ai)** by Anthropic, using Claude Code, as a tooling project. That includes the code, the tests and this README. I directed it, reviewed it, tested it and published it. Ideas borrowed with thanks from [czkawka](https://github.com/qarmin/czkawka), [jdupes](https://codeberg.org/jbruchon/jdupes), [dupeGuru](https://github.com/arsenetar/dupeguru) and [organize](https://github.com/tfeldmann/organize).

Issues and pull requests are welcome.

---

## Disclaimer

This is a free hobby project, provided **as is** with no warranty (see the license). It's deliberately cautious: approval first, holding area, undo, re-checks before every action. Still, **keep backups of anything important**, and read the review page before you approve.

---

## License

[MIT](LICENSE) © phillip8232
