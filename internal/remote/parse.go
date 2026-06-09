package remote

import (
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cosmocode/k8tc/internal/file"
)

// parseLS turns `ls -la` output into file.Info. With fullTime the timestamp is
// parsed from the ISO columns; otherwise ModTime is left zero. A synthesized
// ".." entry is prepended unless dir is the root. Pod paths are always
// slash-separated, so this uses path, not path/filepath.
func parseLS(out, dir string, fullTime bool) []file.Info {
	var files []file.Info
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		fields := strings.Fields(line)
		// mode links owner group size <date columns...> name
		// Both `--full-time` and plain `ls -la` put the name at field index 8.
		if len(fields) < 9 || len(fields[0]) < 10 {
			continue
		}
		mode := fields[0]
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		name := strings.Join(fields[8:], " ")

		var mtime time.Time
		if fullTime {
			// e.g. "2024-01-15 10:30:00.123456789 +0000"
			mtime, _ = time.Parse("2006-01-02 15:04:05.999999999 -0700",
				fields[5]+" "+fields[6]+" "+fields[7])
		}

		// Strip the "-> target" suffix from symlink listings.
		if mode[0] == 'l' {
			if i := strings.Index(name, " -> "); i >= 0 {
				name = name[:i]
			}
		}
		if name == "." || name == ".." {
			continue
		}
		files = append(files, file.Info{
			Name:    name,
			Size:    size,
			Mode:    mode,
			IsDir:   mode[0] == 'd',
			ModTime: mtime,
		})
	}
	file.Sort(files)
	if path.Clean(dir) != "/" {
		files = append([]file.Info{{Name: "..", Mode: "drwxr-xr-x", IsDir: true}}, files...)
	}
	return files
}
