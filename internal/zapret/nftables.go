package zapret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// nftRules describes the firewall rules that hand traffic to nfqws via
// NFQUEUE. We use a dedicated table so cleanup is trivial: `nft delete
// table inet <name>` removes every rule we ever added.
//
// Conceptually we add two rules per protocol family (output + forward) so
// the queue catches both locally-originated traffic and any traffic the box
// might forward (rare on a lampac VPS, but harmless).
type nftRules struct {
	tableName string
	queueNum  int
	tcpPorts  []int
}

// applyNFTables creates the table+chain+rules. Idempotent — drops the existing
// table first so re-runs (e.g. after a config reload) don't accumulate rules.
//
// The shape of the table:
//
//	table inet <name> {
//	  chain output {
//	    type filter hook output priority 0; policy accept;
//	    meta l4proto tcp tcp dport { 443, 80 } ct state established,related queue num <q> bypass
//	  }
//	}
//
// `bypass` lets traffic through if nfqws isn't listening (so the box doesn't
// brick itself when the daemon is restarting). Without it, packets get dropped.
func (r nftRules) apply(ctx context.Context) error {
	if err := requireNFT(ctx); err != nil {
		return err
	}
	// Drop any prior table by the same name. Ignore errors — table may not exist.
	_ = runNFT(ctx, "delete", "table", "inet", r.tableName)

	ports := make([]string, 0, len(r.tcpPorts))
	for _, p := range r.tcpPorts {
		ports = append(ports, fmt.Sprintf("%d", p))
	}
	if len(ports) == 0 {
		ports = []string{"443", "80"}
	}
	portSet := "{ " + strings.Join(ports, ", ") + " }"

	script := fmt.Sprintf(`add table inet %s
add chain inet %s output { type filter hook output priority 0 ; policy accept ; }
add chain inet %s forward { type filter hook forward priority 0 ; policy accept ; }
add rule inet %s output meta l4proto tcp tcp dport %s queue num %d bypass
add rule inet %s forward meta l4proto tcp tcp dport %s queue num %d bypass
`, r.tableName, r.tableName, r.tableName, r.tableName, portSet, r.queueNum, r.tableName, portSet, r.queueNum)

	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nft apply: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// cleanup deletes our table. Safe to call multiple times.
func (r nftRules) cleanup() error {
	// Use a short fresh context — cleanup runs at shutdown so the parent
	// context may already be cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := requireNFT(ctx); err != nil {
		return err
	}
	if err := runNFT(ctx, "delete", "table", "inet", r.tableName); err != nil {
		// "No such file or directory" is fine — table already gone.
		s := strings.ToLower(err.Error())
		if strings.Contains(s, "no such") || strings.Contains(s, "does not exist") {
			return nil
		}
		return err
	}
	return nil
}

// requireNFT verifies that the `nft` binary is available. Returns a friendly
// error so the caller can disable zapret instead of crashing.
func requireNFT(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "nft", "--version")
	if err := cmd.Run(); err != nil {
		return errors.New("zapret: `nft` (nftables) not found in PATH; install nftables or set zapret.enable=false")
	}
	return nil
}

// runNFT runs `nft <args>` and returns stderr-wrapped error.
func runNFT(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "nft", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nft %s: %w (%s)", strings.Join(args, " "), err,
			strings.TrimSpace(stderr.String()))
	}
	return nil
}
