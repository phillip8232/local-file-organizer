# Local File Organizer

A free, local tool that sorts files by rules and suggests deletions you approve.
It runs on Windows, macOS and Linux from one Go codebase with no cloud and no account.

- **Suggest, then confirm.** Nothing moves or is deleted until you approve it.
- **Undo everything.** Deletes go to a holding area for 30 days, then to the OS
  trash. Every batch can be reversed from History.
- **Checked before acting.** Each file is re-checked right before it's touched.
  A file that changed since the scan is skipped. A duplicate is only deleted if
  the copy being kept still exists and both still hash the same.
- **Leaves things alone.** Project folders (`.git`, `go.mod`, `package.json`,
  `.claude`, `default.project.json`, …), system and app folders, symlinks, and
  cloud/online-only files (OneDrive, iCloud) are never moved, deleted or read.

## Use

```
organizer ui            # opens the review page in your browser (recommended)
organizer scan          # index Downloads, Desktop and Documents
organizer report        # see what was found
organizer plan cleanup  # or: organizer plan sort
organizer apply <plan>  # carries out the pre-selected items, after asking
organizer history
organizer undo <batch>
organizer purge         # empty items held past 30 days into the OS trash
organizer where         # where settings, rules and the index live
```

The review page listens on `127.0.0.1` only and needs the one-time link
printed in the terminal.

## Settings

`organizer where` shows the data folder (`%LOCALAPPDATA%\organizer` on Windows).
- `config.yaml`: which folders to organise, extra protected folders, holding
  days, the old-downloads age, and hashing workers (4 for SSD, 1 for HDD).
- `rules.yaml`: sort rules. Edit it in the Rules tab, then preview the result
  on the Sort tab.

## What it finds

| Category | How | Pre-selected |
|---|---|---|
| Duplicates | same size → xxh3 of start/middle/end samples → full BLAKE3; hard links collapsed | yes, one copy kept |
| Empty folders | nothing inside but empty folders or Thumbs.db/.DS_Store | yes |
| Empty files | 0 bytes | yes |
| Junk | `.crdownload`, `.part`, `~$` lock files, all older than a day | yes |
| Old downloads | directly in Downloads, not created or modified in 90 days | no |
| Largest files | top 50 | no |

## Build

```
go test ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o dist/organizer-darwin-arm64 ./cmd/organizer
```

Every target builds from any OS without CGO. macOS trashing goes through
Finder, and the first purge asks for permission to control Finder.

## Not done yet

- Watching folders for new files (fsnotify)
- Moving files between drives (refused with a clear message)
- Similar-image detection
