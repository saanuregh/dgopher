package app

import (
	"slices"

	"github.com/egoist/mygo/ui"
)

// topLevel is the folder choice of the top level: no folder's path
// starts with a /, so none is labeled alike.
const topLevel = "/ (top level)"

// folderField chooses the navigator folder of the form's connection, as
// DBeaver's General page does; shown when its project has folders. A new
// connection moved to another project leaves a folder of the one before.
func folderField(c *ui.Context, f *connForm) {
	if f.project == nil {
		return
	}
	if f.cfg.Folder != "" && !slices.Contains(f.project.Folders, f.cfg.Folder) {
		f.cfg.Folder = ""
	}
	if len(f.project.Folders) == 0 {
		return
	}
	sel := f.cfg.Folder
	if sel == "" {
		sel = topLevel
	}
	ui.Field(c, "Folder", func() {
		if ui.Select(c, &sel, slices.Concat([]string{topLevel}, f.project.Folders)).Label("Folder").Changed() {
			f.cfg.Folder = sel
			if sel == topLevel {
				f.cfg.Folder = ""
			}
		}
	}).Description("Where it shows in the navigator.")
}
