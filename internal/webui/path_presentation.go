package webui

import (
	"path/filepath"
	"strings"

	"pellets/internal/domain"
)

// Stored LocalPaths use canonical slash separators on every platform. The
// project location is its checkout folder, not Git's internal directory.
func repositoryFolder(value domain.LocalPath) string {
	if value.Value == ".git" {
		return ""
	}
	folder := strings.TrimSuffix(value.Value, "/.git")
	// Retain the root separator on an absolute Windows drive.
	if !value.Relative && len(folder) == 2 && folder[1] == ':' {
		return folder + "/"
	}
	return folder
}

func repositoryPath(value domain.LocalPath) string {
	folder := repositoryFolder(value)
	if folder == "" && value.Relative {
		return "Database folder"
	}
	if folder == "" {
		return "/"
	}
	return folder
}

func repositoryFullPath(databaseRoot string, value domain.LocalPath) string {
	folder := repositoryFolder(value)
	if value.Relative {
		root := filepath.ToSlash(databaseRoot)
		if folder == "" {
			return root
		}
		return strings.TrimSuffix(root, "/") + "/" + folder
	}
	if folder == "" {
		return "/"
	}
	return folder
}
