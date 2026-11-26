package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectFolderArg(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "shop.sqlite")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")
	for _, tc := range []struct {
		args []string
		want string
		ok   bool
	}{
		{nil, "", false},
		{[]string{"-flag"}, "", false},
		{[]string{dir}, dir, true},
		// A missing folder still reaches addProject, which reports it.
		{[]string{missing}, missing, true},
		{[]string{file}, "", false},
		{[]string{"file://" + filepath.ToSlash(file)}, "", false},
	} {
		got, ok := projectFolderArg(tc.args)
		if got != tc.want || ok != tc.ok {
			t.Errorf("projectFolderArg(%q) = %q, %v; want %q, %v", tc.args, got, ok, tc.want, tc.ok)
		}
	}
}
