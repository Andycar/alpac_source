package main
import (
	"context"
	"fmt"
	"time"
	"lampac-go/internal/browser"
)
func main() {
	for _, useStealth := range []bool{false, true} {
		fmt.Printf("\n=== stealth=%v ===\n", useStealth)
		eng, _ := browser.Get("rod")
		if err := eng.Available(); err != nil { fmt.Println("avail:", err); return }
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		s, err := eng.NewSession(ctx, browser.SessionOptions{
			Headless: true, UseStealth: useStealth,
			NavigationTimeout: 30 * time.Second,
		})
		if err != nil { fmt.Println("session:", err); cancel(); continue }
		_ = s.Navigate("https://bot.sannysoft.com/")
		probes := []struct{ name, js string }{
			{"navigator.webdriver", `navigator.webdriver`},
			{"navigator.languages", `navigator.languages.join(',')`},
			{"navigator.plugins.length", `navigator.plugins.length`},
			{"window.chrome", `typeof window.chrome`},
			{"chrome.runtime", `typeof (window.chrome && window.chrome.runtime)`},
			{"UA HeadlessChrome", `navigator.userAgent.indexOf('HeadlessChrome') >= 0`},
			{"WebGL vendor", `(function(){var c=document.createElement('canvas').getContext('webgl');if(!c)return 'no-webgl';var ext=c.getExtension('WEBGL_debug_renderer_info');return ext?c.getParameter(ext.UNMASKED_VENDOR_WEBGL):'no-ext'})()`},
		}
		for _, p := range probes {
			var v any
			if err := s.Eval(p.js, &v); err != nil {
				fmt.Printf("  %s: ERR %v\n", p.name, err)
				continue
			}
			fmt.Printf("  %s = %v\n", p.name, v)
		}
		s.Close()
		cancel()
	}
}
