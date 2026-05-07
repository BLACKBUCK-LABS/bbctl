package shell

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FileRef represents an @ file reference found in a curl command.
type FileRef struct {
	Original string // original @/path/to/file
	Path     string // /path/to/file (without @)
	Location string // "ec2", "local", "postman", "unknown"
}

// DetectFileRefs finds all @ file references in a curl command.
func DetectFileRefs(command string) []FileRef {
	re := regexp.MustCompile(`@([^\s'"]+|"[^"]+"|'[^']+')`)
	matches := re.FindAllStringSubmatch(command, -1)

	var refs []FileRef
	for _, m := range matches {
		raw := m[1]
		path := strings.Trim(raw, `"'`)

		ref := FileRef{
			Original: "@" + raw,
			Path:     path,
		}

		switch {
		case strings.HasPrefix(path, "postman-cloud://"):
			ref.Location = "postman"
		case fileExistsLocally(path):
			ref.Location = "local"
		case isEC2Path(path):
			ref.Location = "ec2"
		case isLocalPath(path):
			ref.Location = "local"
		default:
			ref.Location = "unknown"
		}

		refs = append(refs, ref)
	}
	return refs
}

func fileExistsLocally(path string) bool {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		path = filepath.Join(home, path[2:])
	}
	_, err := os.Stat(path)
	return err == nil
}

func isEC2Path(path string) bool {
	ec2Prefixes := []string{
		"/tmp/", "/opt/", "/var/", "/home/",
		"/etc/", "/srv/", "/app/", "/data/",
	}
	for _, prefix := range ec2Prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func isLocalPath(path string) bool {
	return strings.HasPrefix(path, "/Users/") ||
		strings.HasPrefix(path, "~/") ||
		strings.HasPrefix(path, "./") ||
		strings.HasPrefix(path, "../") ||
		(!strings.HasPrefix(path, "/") && !strings.Contains(path, "://"))
}

// RewriteCommand replaces local @path references in a curl command
// with their EC2 destination paths.
func RewriteCommand(command string, rewrites map[string]string) string {
	result := command
	for localPath, ec2Path := range rewrites {
		result = strings.ReplaceAll(result, "@"+localPath, "@"+ec2Path)
		result = strings.ReplaceAll(result, `@"`+localPath+`"`, "@"+ec2Path)
		result = strings.ReplaceAll(result, `@'`+localPath+`'`, "@"+ec2Path)
	}
	return result
}

// SuggestEC2Path suggests a destination path on EC2 for a local file.
func SuggestEC2Path(localPath string) string {
	return "/tmp/" + filepath.Base(localPath)
}
