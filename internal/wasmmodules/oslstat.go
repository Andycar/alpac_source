package wasmmodules

import "os"

// osLstat is wrapped here purely so Watcher's tests can stub it out without
// fighting the standard-lib symbol.
var osLstat = os.Lstat
