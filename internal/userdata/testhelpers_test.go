package userdata

import "testing"

// resetHTTPAPIGlobals is a no-op isolation shim: the persistence tests isolate
// via t.TempDir()/LAMPAC_GO_HOME, not the httpapi service globals the original
// (httpapi) helper reset. Kept so the moved test bodies compile unchanged.
func resetHTTPAPIGlobals(t *testing.T) { t.Helper() }
