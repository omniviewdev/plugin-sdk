package types

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The host renames and removes a plugin's directory to update or uninstall the
// plugin. Windows refuses both while any file inside is open, so the loaders
// must have closed their file when they return, on every path, and never leave
// that to the garbage collector's finalizer.

// openDescriptors counts the process's open file descriptors through /dev/fd.
// ok is false where the system cannot list them (Windows).
func openDescriptors() (count int, ok bool) {
	entries, err := os.ReadDir("/dev/fd")
	return len(entries), err == nil
}

// requireFileClosed runs call repeatedly and requires that afterwards the
// process holds no more descriptors than before and that the plugin directory
// can be renamed, which is the move the host makes (Windows fails it while a
// file in the directory is open).
func requireFileClosed(t *testing.T, dir string, call func()) {
	t.Helper()
	// A leaked file is closed when the collector runs its finalizer. Hold the
	// collector off so that the result never depends on when that happens.
	previous := debug.SetGCPercent(-1)
	t.Cleanup(func() { debug.SetGCPercent(previous) })

	const calls = 4
	before, listed := openDescriptors()
	for range calls {
		call()
	}
	if listed {
		after, _ := openDescriptors()
		require.Equal(t, before, after, "the loader left a file descriptor open after it returned")
	}
	require.NoError(
		t,
		os.Rename(dir, dir+".renamed"),
		"the plugin directory cannot be renamed while a file in it is open",
	)
}

// pluginDirectory returns a directory holding the named files.
func pluginDirectory(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "plugin")
	require.NoError(t, os.Mkdir(dir, 0o700))
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	return dir
}

func TestLoadPluginMetadata_ClosesItsFile(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{name: "valid", files: map[string]string{"plugin.yaml": "id: fixture\nversion: 1.2.3\n"}},
		{
			name:    "empty",
			files:   map[string]string{"plugin.yaml": ""},
			wantErr: "error parsing plugin config file",
		},
		{
			name:    "not yaml",
			files:   map[string]string{"plugin.yaml": "id: [unterminated\n"},
			wantErr: "error parsing plugin config file",
		},
		{name: "missing", wantErr: "error loading plugin config file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := pluginDirectory(t, tc.files)

			meta, err := LoadPluginMetadata(dir)
			if tc.wantErr == "" {
				require.NoError(t, err)
				require.Equal(t, "fixture", meta.ID)
				require.Equal(t, "1.2.3", meta.Version)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
				require.Zero(t, meta.ID)
			}

			requireFileClosed(t, dir, func() { _, _ = LoadPluginMetadata(dir) })
		})
	}
}

func TestLoadPluginMarkdown_ClosesItsFile(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		want    string
		wantErr string
	}{
		{name: "valid", files: map[string]string{"README.md": "# Fixture\n"}, want: "# Fixture\n"},
		{name: "empty", files: map[string]string{"README.md": ""}},
		{name: "missing", wantErr: "error loading plugin markdown file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := pluginDirectory(t, tc.files)

			markdown, err := LoadPluginMarkdown(dir)
			if tc.wantErr == "" {
				require.NoError(t, err)
				require.Equal(t, tc.want, markdown)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
				require.Empty(t, markdown)
			}

			requireFileClosed(t, dir, func() { _, _ = LoadPluginMarkdown(dir) })
		})
	}
}

// openFile stands in for an open file: it counts its Close calls, and its
// reads and its close fail on demand.
type openFile struct {
	reader   io.Reader
	closeErr error
	closed   int
}

func (f *openFile) Read(p []byte) (int, error) { return f.reader.Read(p) }

func (f *openFile) Close() error {
	f.closed++
	return f.closeErr
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

var (
	errRead  = errors.New("read failed")
	errClose = errors.New("close failed")
)

func TestDecodePluginMetadata_ClosesTheFileOnEveryPath(t *testing.T) {
	cases := []struct {
		name         string
		file         *openFile
		wantErr      string
		wantErrIs    error
		wantErrIsNot error
	}{
		{name: "decoded", file: &openFile{reader: strings.NewReader("id: fixture\n")}},
		{
			name:      "decoded but the close fails",
			file:      &openFile{reader: strings.NewReader("id: fixture\n"), closeErr: errClose},
			wantErr:   "error closing plugin config file",
			wantErrIs: errClose,
		},
		{
			name:         "not decodable, so the close failure does not replace the reason",
			file:         &openFile{reader: strings.NewReader("id: [unterminated\n"), closeErr: errClose},
			wantErr:      "error parsing plugin config file",
			wantErrIsNot: errClose,
		},
		{
			name:    "unreadable",
			file:    &openFile{reader: failingReader{err: errRead}},
			wantErr: "error parsing plugin config file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := decodePluginMetadata(tc.file)

			require.Equal(t, 1, tc.file.closed, "the file must be closed exactly once")
			if tc.wantErr == "" {
				require.NoError(t, err)
				require.Equal(t, "fixture", meta.ID)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
			require.Zero(t, meta.ID, "an error comes with no metadata")
			if tc.wantErrIs != nil {
				require.ErrorIs(t, err, tc.wantErrIs)
			}
			if tc.wantErrIsNot != nil {
				require.NotErrorIs(t, err, tc.wantErrIsNot)
			}
		})
	}
}

func TestReadPluginMarkdown_ClosesTheFileOnEveryPath(t *testing.T) {
	cases := []struct {
		name         string
		file         *openFile
		wantErr      string
		wantErrIs    error
		wantErrIsNot error
	}{
		{name: "read", file: &openFile{reader: strings.NewReader("# Fixture\n")}},
		{
			name:      "read but the close fails",
			file:      &openFile{reader: strings.NewReader("# Fixture\n"), closeErr: errClose},
			wantErr:   "error closing plugin markdown file",
			wantErrIs: errClose,
		},
		{
			name:         "unreadable, so the close failure does not replace the reason",
			file:         &openFile{reader: failingReader{err: errRead}, closeErr: errClose},
			wantErr:      "error reading plugin markdown file",
			wantErrIs:    errRead,
			wantErrIsNot: errClose,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			markdown, err := readPluginMarkdown(tc.file)

			require.Equal(t, 1, tc.file.closed, "the file must be closed exactly once")
			if tc.wantErr == "" {
				require.NoError(t, err)
				require.Equal(t, "# Fixture\n", markdown)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
			require.Empty(t, markdown, "an error comes with no markdown")
			if tc.wantErrIs != nil {
				require.ErrorIs(t, err, tc.wantErrIs)
			}
			if tc.wantErrIsNot != nil {
				require.NotErrorIs(t, err, tc.wantErrIsNot)
			}
		})
	}
}
