package utils

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omniviewdev/plugin-sdk/internal/testutil"
)

func TestDeexpandTildeHomeBoundary(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home-directory")
	testutil.SetHome(t, home)
	outside := filepath.Dir(home)
	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "outside_shorter_home", path: outside, want: outside},
		{
			name: "outside_longer_home",
			path: filepath.Join(outside, "other-home-directory", "config"),
			want: filepath.Join(outside, "other-home-directory", "config"),
		},
		{
			name: "sibling_prefix",
			path: filepath.Join(home+"-other", "config"),
			want: filepath.Join(home+"-other", "config"),
		},
		{name: "home", path: home, want: "~"},
		{name: "descendant", path: filepath.Join(home, "config"), want: filepath.Join("~", "config")},
		{name: "empty", path: "", want: ""},
		{name: "relative", path: "config", want: "config"},
		{name: "already_compact", path: "~/config", want: "~/config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, DeexpandTilde(tc.path))
		})
	}
}

func TestDeexpandTildeHomeSeparator(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name string
		home string
	}{
		{name: "trailing_separator", home: home + string(filepath.Separator)},
		{name: "filesystem_root", home: filepath.VolumeName(home) + string(filepath.Separator)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testutil.SetHome(t, tc.home)
			require.Equal(t, filepath.Join("~", "config"), DeexpandTilde(filepath.Join(tc.home, "config")))
		})
	}
}

func TestExpandTildeShortInputs(t *testing.T) {
	home := t.TempDir()
	testutil.SetHome(t, home)
	cases := []struct {
		path string
		want string
	}{
		{path: "", want: ""},
		{path: ".", want: "."},
		{path: "~", want: "~"},
		{path: "~user/config", want: "~user/config"},
		{path: "~/", want: home},
		{path: "~/config", want: filepath.Join(home, "config")},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got, err := ExpandTilde(tc.path)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestTildeHelpersWithoutHome(t *testing.T) {
	testutil.SetHome(t, "")
	require.Equal(t, "outside/config", DeexpandTilde("outside/config"))
	_, err := ExpandTilde("~/config")
	require.Error(t, err)
	got, err := ExpandTilde("config")
	require.NoError(t, err)
	require.Equal(t, "config", got)
}
