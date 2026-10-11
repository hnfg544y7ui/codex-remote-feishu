package threadcatalogcontract

// Desktop projects are display groups. Their IDs never replace workspace paths
// used for execution, claims or access checks.
type DesktopProject struct {
	ID        string
	Name      string
	RootPaths []string
}

type DesktopProjectSnapshot struct {
	Available bool
	Projects  []DesktopProject
	// An empty value is an explicit projectless assignment and prevents root inference.
	ThreadProjectIDs map[string]string
}

type DesktopProjectCatalog interface {
	DesktopProjects() (DesktopProjectSnapshot, error)
}
