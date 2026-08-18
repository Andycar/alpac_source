package httpapi

import (
	"strings"
	"testing"
)

// resetBrandingDefaults restores the built-in defaults so independent test
// cases don't see each other's overrides.
func resetBrandingDefaults(t *testing.T) {
	t.Helper()
	SetBranding(Branding{})
}

func TestApplyPublicBrandingJS_DefaultsLeaveDefaults(t *testing.T) {
	resetBrandingDefaults(t)
	src := `var x = title_online: { ru: 'Alpac Онлайн', uk: 'Alpac Онлайн', en: 'Alpac Online', zh: 'Alpac 在线' };
window.lampac_plugin = true;
var manifst = { type: 'video', version: '1.6.7', name: 'Lampac' };`
	out := applyPublicBrandingJS(src)
	if !strings.Contains(out, `name: 'Alpac'`) {
		t.Errorf("name not rewritten:\n%s", out)
	}
	// Default OnlineNameRU is "Alpac Онлайн" — rewrite is idempotent.
	if !strings.Contains(out, `'Alpac Онлайн'`) {
		t.Errorf("default ru not preserved:\n%s", out)
	}
	if !strings.Contains(out, `version: '0.5'`) {
		t.Errorf("version not stamped:\n%s", out)
	}
}

func TestApplyPublicBrandingJS_CustomOnlineNames(t *testing.T) {
	resetBrandingDefaults(t)
	SetBranding(Branding{
		OnlineNameRU: "Тест Онлайн",
		OnlineNameEN: "Test Online",
	})
	src := `var x = title_online: { ru: 'Alpac Онлайн', uk: 'Alpac Онлайн', en: 'Alpac Online', zh: 'Alpac 在线' };`
	out := applyPublicBrandingJS(src)
	if !strings.Contains(out, `ru: 'Тест Онлайн'`) {
		t.Errorf("ru not overridden:\n%s", out)
	}
	if !strings.Contains(out, `en: 'Test Online'`) {
		t.Errorf("en not overridden:\n%s", out)
	}
	// Unset fields fall back to default ("Alpac Онлайн" for uk, "Alpac 在线" for zh).
	if !strings.Contains(out, `uk: 'Alpac Онлайн'`) {
		t.Errorf("uk should fall back to default:\n%s", out)
	}
	if !strings.Contains(out, `zh: 'Alpac 在线'`) {
		t.Errorf("zh should fall back to default:\n%s", out)
	}
	// Cleanup so later tests start fresh.
	resetBrandingDefaults(t)
}

func TestApplyPublicBrandingJS_CustomBrandName(t *testing.T) {
	resetBrandingDefaults(t)
	SetBranding(Branding{Name: "MyBrand"})
	src := `var src_name = 'Lampac';
var foo = { name: 'Lampac', type: 'video' };`
	out := applyPublicBrandingJS(src)
	if !strings.Contains(out, `name: 'MyBrand'`) {
		t.Errorf("name: 'Lampac' should be rewritten:\n%s", out)
	}
	if strings.Contains(out, `'Lampac'`) {
		t.Errorf("global Lampac should be gone:\n%s", out)
	}
	resetBrandingDefaults(t)
}

// TestApplyPublicBrandingJS_PreservesLampacHeaderNames is a regression test for
// the LG/webOS "SyntaxError: Unexpected token '}'" at launch. The default brand
// name is "Alpac", so the blanket "Lampac"→Name replace used to rewrite the
// literal header name X-Lampac-Token into X-Alpac-Token — colliding with the
// sibling X-Alpac-Token key in online.js (lampacAuthHeaders) and producing a
// duplicate object property. Duplicate data keys are legal in ES2015+ but a
// SyntaxError under ES5 strict mode, so old webOS/Tizen browsers failed to parse
// the whole online plugin. Header names must survive branding verbatim.
func TestApplyPublicBrandingJS_PreservesLampacHeaderNames(t *testing.T) {
	resetBrandingDefaults(t)
	SetBranding(Branding{Name: "Alpac"})
	src := `'use strict';
function lampacAuthHeaders(tok){
  return {'X-Lampac-Token': tok, 'X-Alpac-Token': tok};
}`
	out := applyPublicBrandingJS(src)
	// The legacy header name must survive verbatim — the server middleware and
	// CORS allow-list match it literally.
	if !strings.Contains(out, `'X-Lampac-Token': tok`) {
		t.Errorf("X-Lampac-Token header name was corrupted by branding:\n%s", out)
	}
	// It must NOT collapse into a duplicate-key literal (the ES5 SyntaxError).
	if strings.Contains(out, `{'X-Alpac-Token': tok, 'X-Alpac-Token': tok}`) {
		t.Errorf("duplicate X-Alpac-Token key (ES5 SyntaxError) produced by branding:\n%s", out)
	}
	resetBrandingDefaults(t)
}

func TestApplyPublicBrandingJS_EscapeQuotes(t *testing.T) {
	resetBrandingDefaults(t)
	SetBranding(Branding{OnlineNameRU: "It's Online"})
	src := `var x = title_online: { ru: 'Alpac Онлайн', uk: 'Alpac Онлайн', en: 'Alpac Online', zh: 'Alpac 在线' };`
	out := applyPublicBrandingJS(src)
	if !strings.Contains(out, `ru: 'It\'s Online'`) {
		t.Errorf("single quote not escaped:\n%s", out)
	}
	resetBrandingDefaults(t)
}

func TestApplyPublicBrandingHTML_V2Variant(t *testing.T) {
	resetBrandingDefaults(t)
	SetBranding(Branding{HTMLTitleV2: "Мой Сервис - Каталог"})
	src := `<html><head><title>Alpac - Каталог фильмов и сериалов</title>
<meta property="og:title" content="Alpac - Каталог фильмов и сериалов">
</head></html>`
	out := applyPublicBrandingHTML(src, "v2")
	if !strings.Contains(out, `<title>Мой Сервис - Каталог</title>`) {
		t.Errorf("title not overridden:\n%s", out)
	}
	if !strings.Contains(out, `content="Мой Сервис - Каталог"`) {
		t.Errorf("og:title not overridden:\n%s", out)
	}
	resetBrandingDefaults(t)
}

func TestApplyPublicBrandingHTML_LiteVariant(t *testing.T) {
	resetBrandingDefaults(t)
	SetBranding(Branding{HTMLTitleLite: "Лайт"})
	src := `<html><head><title>Alpac</title></head></html>`
	out := applyPublicBrandingHTML(src, "lite")
	if !strings.Contains(out, `<title>Лайт</title>`) {
		t.Errorf("lite title not overridden:\n%s", out)
	}
	resetBrandingDefaults(t)
}

func TestApplyPublicBrandingHTML_HTMLEscape(t *testing.T) {
	resetBrandingDefaults(t)
	SetBranding(Branding{HTMLTitleLite: "<script>alert(1)</script>"})
	src := `<title>Alpac</title>`
	out := applyPublicBrandingHTML(src, "lite")
	if strings.Contains(out, "<script>") {
		t.Errorf("HTML not escaped:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected &lt;script&gt; escape:\n%s", out)
	}
	resetBrandingDefaults(t)
}

func TestSetBranding_PartialMerge(t *testing.T) {
	resetBrandingDefaults(t)
	SetBranding(Branding{Name: "X"})
	b := currentBranding()
	if b.Name != "X" {
		t.Errorf("Name = %q, want X", b.Name)
	}
	// Other fields fall back to defaults.
	if b.OnlineNameRU != brandingDefaults.OnlineNameRU {
		t.Errorf("OnlineNameRU = %q, want default %q", b.OnlineNameRU, brandingDefaults.OnlineNameRU)
	}
	if b.Version != brandingDefaults.Version {
		t.Errorf("Version = %q, want default %q", b.Version, brandingDefaults.Version)
	}
	resetBrandingDefaults(t)
}

func TestSaveAndLoadBrandingOverride(t *testing.T) {
	resetBrandingDefaults(t)
	defer resetBrandingDefaults(t)

	dir := t.TempDir()
	if err := saveBrandingOverride(dir, Branding{Name: "Persisted"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	b, ok, err := loadBrandingOverride(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !ok {
		t.Fatal("override should exist")
	}
	if b.Name != "Persisted" {
		t.Errorf("Name = %q, want Persisted", b.Name)
	}
	// SetBranding should have run, so the in-memory store reflects it.
	if currentBranding().Name != "Persisted" {
		t.Errorf("in-memory Name = %q, want Persisted", currentBranding().Name)
	}
}

func TestJSEscapeSingle(t *testing.T) {
	cases := map[string]string{
		`hello`:        `hello`,
		`it's`:         `it\'s`,
		`back\slash`:   `back\\slash`,
		"line\nbreak":  "line break",
		"crlf\r\ntest": "crlf test",
	}
	for in, want := range cases {
		if got := jsEscapeSingle(in); got != want {
			t.Errorf("jsEscapeSingle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJSONStringLiteral(t *testing.T) {
	cases := map[string]string{
		"hello": `"hello"`,
		`a"b`:   `"a\"b"`,
		"кир":   `"кир"`,
	}
	for in, want := range cases {
		if got := jsonStringLiteral(in); got != want {
			t.Errorf("jsonStringLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}
