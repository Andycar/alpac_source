package browser

// Apply wires the registry from the BrowserPool section of config.
// Called from server startup and on hot-reload.
//
// Parameters mirror BrowserPoolConfig but are decoupled to avoid an
// import cycle (internal/config → internal/browser would be tempting
// otherwise).
func Apply(engine string, balancerOverrides map[string]string) {
	SetDefault(engine)
	SetBalancerOverrides(balancerOverrides)
}
