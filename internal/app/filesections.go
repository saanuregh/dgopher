package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dgopher/internal/datamodel"
	"dgopher/internal/project"
	"dgopher/internal/ui/dashboard"
)

// A project's files show in three sections of the navigator's files pane:
// Queries, Dashboards and Data Models. Each is a folder of the project,
// whose files and folders, at any depth, the navigator shows as a tree, as
// DBeaver's Scripts. A file's node is named by its / path relative to its
// section's folder, and so is a folder's.

// fileSection is one of the sections of a project's files.
type fileSection struct {
	heading nodeKind // the section's own node
	file    nodeKind // the nodes of its files
	noun    string   // one of its files, in a sentence, as "query"
	plural  string   // as "queries"; it names the section in the layout too
	ext     string   // its files' extension
}

var (
	queriesSection    = fileSection{heading: nodeQueries, file: nodeQueryFile, noun: "query", plural: "queries", ext: ".sql"}
	dashboardsSection = fileSection{heading: nodeDashboards, file: nodeDashboard, noun: "dashboard", plural: "dashboards", ext: ".json"}
	modelsSection     = fileSection{heading: nodeModels, file: nodeModel, noun: "data model", plural: "data models", ext: ".json"}
	fileSections      = [...]fileSection{queriesSection, dashboardsSection, modelsSection}
)

// sectionOf is the section of a node: its heading, a file or a folder of
// it; false for other nodes.
func sectionOf(n navNode) (fileSection, bool) {
	kind := n.kind
	if kind == nodeFileFolder {
		kind = n.section
	}
	for _, s := range fileSections {
		if kind == s.heading || kind == s.file {
			return s, true
		}
	}
	return fileSection{}, false
}

// root is the folder of a project that holds the section's files.
func (s fileSection) root(p *project.Project) string {
	switch s.heading {
	case nodeDashboards:
		return filepath.Join(p.Dir, dashboard.Folder)
	case nodeModels:
		return filepath.Join(p.Dir, datamodel.Folder)
	}
	return p.Queries
}

// path is the absolute path of a file or a folder of the section, by its
// path relative to the section's folder.
func (s fileSection) path(p *project.Project, rel string) string {
	return filepath.Join(s.root(p), filepath.FromSlash(rel))
}

// rel is the path relative to the section's folder of a file or a folder
// in it; false for one outside it.
func (s fileSection) rel(p *project.Project, path string) (string, bool) {
	rel, err := filepath.Rel(s.root(p), path)
	if err != nil || !filepath.IsLocal(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// parent is the folder of the section a file or a folder at path is in,
// "" for its top.
func (s fileSection) parent(p *project.Project, path string) string {
	rel, _ := s.rel(p, path)
	return project.FolderParent(rel)
}

// fileNode is the node of a file of the section.
func (s fileSection) fileNode(p *project.Project, rel string) navNode {
	return navNode{kind: s.file, projectDir: p.Dir, name: rel}
}

// folderNode is the node of a folder of the section, "" for its heading.
func (s fileSection) folderNode(p *project.Project, rel string) navNode {
	if rel == "" {
		return navNode{kind: s.heading, projectDir: p.Dir}
	}
	return navNode{kind: nodeFileFolder, projectDir: p.Dir, section: s.heading, name: rel}
}

// filePath is the absolute path of the file a node of a section names.
func (a *App) filePath(p *project.Project, n navNode) string {
	s, _ := sectionOf(n)
	return s.path(p, n.name)
}

// sectionFiles is what a section of a project holds: the query files as
// the last scan found them, the dashboards and models as last read.
func (a *App) sectionFiles(p *project.Project, s fileSection) project.Listing {
	if s.heading == nodeQueries {
		return queryListing(p)
	}
	return a.readFiles(p, s).Listing
}

// queryListing is what the queries' section holds: the query files and
// their folders, without the folders of the dashboards and the models,
// which a queries folder set to the project's own holds too, for those
// to be neither moved nor deleted from there.
func queryListing(p *project.Project) project.Listing {
	l := project.Listing{Files: p.Files, Folders: p.QueryFolders}
	var others []string
	for _, s := range []fileSection{dashboardsSection, modelsSection} {
		if rel, ok := queriesSection.rel(p, s.root(p)); ok {
			others = append(others, rel)
		}
	}
	if others == nil {
		return l
	}
	elsewhere := func(path string) bool {
		return slices.ContainsFunc(others, func(root string) bool { return project.UnderFolder(path, root) })
	}
	l.Files = slices.DeleteFunc(slices.Clone(l.Files), elsewhere)
	l.Folders = slices.DeleteFunc(slices.Clone(l.Folders), elsewhere)
	return l
}

// fileName is what a file of a section shows as: a query file's name
// without .sql, a dashboard's or a model's own name.
func (a *App) fileName(p *project.Project, s fileSection, rel string) string {
	if s.heading == nodeQueries {
		return strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	}
	if name, ok := a.readFiles(p, s).names[rel]; ok {
		return name
	}
	return filepath.Base(rel)
}

// fileModified is when a file of a section last changed.
func (a *App) fileModified(p *project.Project, s fileSection, rel string) time.Time {
	if s.heading == nodeQueries {
		return p.FileConnections[rel].Mod
	}
	return a.readFiles(p, s).Modified[rel]
}

// readFiles are the files of a section of dashboards or models, with the
// names they hold; read again once one of its folders changes, as a file
// comes, goes or is saved, which writes a new file in its place. A file
// that does not read is left out.
func (a *App) readFiles(p *project.Project, s fileSection) readFiles {
	root := s.root(p)
	if l, ok := a.fileLists[root]; ok && l.current(root) {
		return l
	}
	listed, _ := project.List(root, s.ext)
	l := readFiles{names: map[string]string{}, folders: map[string]time.Time{}}
	for _, rel := range listed.Files {
		if name, err := s.loadName(s.path(p, rel)); err == nil {
			l.Files = append(l.Files, rel)
			l.names[rel] = name
		}
	}
	l.Folders, l.Modified = listed.Folders, listed.Modified
	for _, rel := range append([]string{""}, listed.Folders...) {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			l.folders[rel] = info.ModTime()
		}
	}
	if a.fileLists == nil {
		a.fileLists = map[string]readFiles{}
	}
	a.fileLists[root] = l
	return l
}

// readFiles are a section's files as last read: the listing, the names
// the files hold, and when each folder, "" for the section's own, last
// changed.
type readFiles struct {
	project.Listing
	names   map[string]string
	folders map[string]time.Time
}

// current reports whether no folder of the files changed since they were
// read: a folder made or removed changes the one it is in.
func (l readFiles) current(root string) bool {
	if len(l.folders) == 0 {
		return false
	}
	for rel, was := range l.folders {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !info.ModTime().Equal(was) {
			return false
		}
	}
	return true
}

// loadName reads the name a dashboard's or a model's file holds.
func (s fileSection) loadName(path string) (string, error) {
	if s.heading == nodeModels {
		m, err := datamodel.Load(path)
		if err != nil {
			return "", err
		}
		return m.Name, nil
	}
	d, err := dashboard.Load(path)
	if err != nil {
		return "", err
	}
	return d.Name, nil
}

// fileNamesAndPaths are the names of a section's files of dashboards or
// models, with their absolute paths, in the order of their paths.
func (a *App) fileNamesAndPaths(p *project.Project, s fileSection) (names, paths []string) {
	l := a.readFiles(p, s)
	for _, rel := range l.Files {
		names, paths = append(names, l.names[rel]), append(paths, s.path(p, rel))
	}
	return names, paths
}
