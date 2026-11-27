package mongoserver

import "testing"

func TestURIOverride(t *testing.T) {
	t.Setenv("DGOPHER_MONGO_URL", "")
	if got := uri("DGOPHER_MONGO_URL", primaryURI); got != primaryURI {
		t.Errorf("unset: %q", got)
	}
	t.Setenv("DGOPHER_MONGO_URL", "mongodb://elsewhere")
	if got := uri("DGOPHER_MONGO_URL", primaryURI); got != "mongodb://elsewhere" {
		t.Errorf("set: %q", got)
	}
}
