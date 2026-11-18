package project

import (
	"errors"
	"slices"
	"strings"

	"dgopher/internal/db"
)

// Folder is a folder of the navigator's connections, keyed in
// dgopher.json by its path, as DBeaver's data-sources.json keys them.
// It holds nothing yet: a folder's own settings, such as DBeaver's
// description, would go here.
type Folder struct{}

// CleanFolderName trims a folder's name, or says why it cannot be one: a
// "/" would split it into two folders.
func CleanFolderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "" || name == "." || name == "..":
		return "", errors.New("A folder needs a name.")
	case strings.ContainsAny(name, `/\`):
		return "", errors.New(`A folder's name may not hold / or \.`)
	}
	return name, nil
}

// cleanFolderPath reads a path written by hand as the folders it means:
// each name trimmed, and empty ones, as of a doubled or trailing /, gone.
func cleanFolderPath(path string) string {
	var names []string
	for name := range strings.SplitSeq(path, "/") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "/")
}

// FolderParent is the path of the folder a folder is in, "" for the top.
func FolderParent(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return ""
}

// FolderBase is a folder's own name, the last of its path.
func FolderBase(path string) string { return path[strings.LastIndexByte(path, '/')+1:] }

// UnderFolder reports whether path is folder or a folder inside it; every
// path is under the top, "".
func UnderFolder(path, folder string) bool {
	return folder == "" || strings.HasPrefix(path, folder) && (len(path) == len(folder) || path[len(folder)] == '/')
}

// MovedFolder is where path goes when the folder from moves to to: the
// same place under to, and false when path is not under from.
func MovedFolder(path, from, to string) (string, bool) {
	if from == "" || !UnderFolder(path, from) {
		return "", false
	}
	rest := strings.TrimPrefix(path[len(from):], "/")
	switch {
	case rest == "":
		return to, true
	case to == "":
		return rest, true
	}
	return to + "/" + rest, true
}

// MergeFolders lists, sorted and once each, the folders listed, those
// used, and the folders each of them is in.
func MergeFolders(listed []string, used ...string) []string {
	var out []string
	for _, path := range slices.Concat(listed, used) {
		for ; path != ""; path = FolderParent(path) {
			out = append(out, path)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// cleanFolders cleans, in place, the paths of a project file's folders
// and of its connections' folders.
func cleanFolders(pc *Config) {
	for path, f := range pc.Folders {
		if clean := cleanFolderPath(path); clean != path {
			delete(pc.Folders, path)
			if clean != "" {
				pc.Folders[clean] = f
			}
		}
	}
	for i := range pc.Connections {
		pc.Connections[i].Folder = cleanFolderPath(pc.Connections[i].Folder)
	}
}

// connFolders are the folders connections are in.
func connFolders(cfgs []db.Config) []string {
	out := make([]string, len(cfgs))
	for i, cfg := range cfgs {
		out[i] = cfg.Folder
	}
	return out
}
