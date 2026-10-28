package platform

import (
	"bufio"
	"os"
	"strings"
)

var realFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "f2fs": true, "zfs": true,
	"ntfs": true, "ntfs3": true, "fuseblk": true, "exfat": true, "vfat": true, "jfs": true, "reiserfs": true, "bcachefs": true,
}

// Volumes lists mounted local filesystems, skipping system and snap mounts.
func Volumes() ([]Volume, error) {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []Volume
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 3 || !realFS[fields[2]] || seen[fields[0]] {
			continue
		}
		mnt := strings.ReplaceAll(fields[1], `\040`, " ")
		if strings.HasPrefix(mnt, "/boot") || strings.HasPrefix(mnt, "/snap") || strings.HasPrefix(mnt, "/var/lib") {
			continue
		}
		v, ok := statVolume(mnt)
		if !ok {
			continue
		}
		seen[fields[0]] = true
		v.FS = fields[2]
		out = append(out, v)
	}
	return out, s.Err()
}
