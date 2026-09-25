package commands

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatUploadFile_RegularFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(p, []byte("hello world"), 0644))

	size, executable, err := statUploadFile(p)
	require.NoError(t, err)
	assert.Equal(t, int64(11), size)
	assert.False(t, executable)
}

func TestStatUploadFile_ExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file mode bits only")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "run.sh")
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"), 0755))

	_, executable, err := statUploadFile(p)
	require.NoError(t, err)
	assert.True(t, executable)
}

func TestStatUploadFile_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	_, _, err := statUploadFile(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
}

func TestStatUploadFile_RejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated perms on windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0644))
	link := filepath.Join(dir, "link.txt")
	require.NoError(t, os.Symlink(target, link))

	_, _, err := statUploadFile(link)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
}

func TestStatUploadFile_RejectsOversize(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.bin")
	f, err := os.Create(p)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(maxUploadSize+1))
	require.NoError(t, f.Close())

	_, _, err = statUploadFile(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds the 5 GiB upload limit")
}

func TestStatUploadFile_MissingFile(t *testing.T) {
	_, _, err := statUploadFile("/nonexistent/path/does-not-exist")
	require.Error(t, err)
}

func TestHashFile_MatchesKnownSHA256(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(p, []byte("hello world"), 0644))

	got, err := hashFile(p)
	require.NoError(t, err)
	// echo -n "hello world" | sha256sum
	assert.Equal(t, "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9", got)
}

func TestResolveRemotePath(t *testing.T) {
	cases := []struct {
		name, remote, local, want, wantErr string
	}{
		{"absolute file path unchanged", "/data/in/out.csv", "/local/report.csv", "/data/in/out.csv", ""},
		{"trailing slash appends filename", "/data/in/", "/local/report.csv", "/data/in/report.csv", ""},
		{"empty path rejected", "", "/local/report.csv", "", "must be an absolute path"},
		{"relative path rejected", "data/out.csv", "/local/report.csv", "", "must be an absolute path"},
		{"bare slash plus filename", "/", "/local/report.csv", "/report.csv", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRemotePath(tc.remote, tc.local)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRunUpload_TicketFlagRejected(t *testing.T) {
	uploadTicket = "REQ-123"
	defer func() { uploadTicket = "" }()

	err := runUpload(uploadCmd, []string{"i-abc", "/tmp/does-not-matter", "/data/x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--ticket is no longer supported for upload")
}

func TestRunUpload_ExpiredBoltTokenFailsFast(t *testing.T) {
	dir := t.TempDir()
	// config.DefaultConfigDir() reads os.UserHomeDir(), which on unix reads
	// $HOME — there's no dedicated BBCTL_CONFIG_DIR override, so redirect HOME.
	t.Setenv("HOME", dir)
	uploadTicket = ""

	f := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(f, []byte("hi"), 0644))

	err := runUpload(uploadCmd, []string{"i-abc", f, "/data/x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bbctl login")
}
