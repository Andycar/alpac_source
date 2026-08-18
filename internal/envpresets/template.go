package envpresets

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// {{key}} placeholders. Простой движок: только подстановка, без условных
// блоков и циклов. Достаточно для PAC-файлов и unit-файлов systemd.
var tmplRe = regexp.MustCompile(`{{\s*([a-z_][a-z0-9_]*)\s*}}`)

// RenderString замещает {{key}} в s значениями из vars + builtins.
// Если key неизвестен — placeholder оставляется КАК ЕСТЬ (чтобы при отладке
// было видно что параметр не задан, а не silently заменено пустотой).
func RenderString(s string, vars map[string]string) string {
	return tmplRe.ReplaceAllStringFunc(s, func(match string) string {
		key := tmplRe.FindStringSubmatch(match)[1]
		if v, ok := vars[key]; ok {
			return v
		}
		return match
	})
}

// MissingKeys возвращает список {{key}} placeholder-ов в s которых нет в vars.
// Полезно показать пользователю в форме перед install — какие параметры обязательно
// нужно заполнить.
func MissingKeys(s string, vars map[string]string) []string {
	seen := map[string]bool{}
	for _, m := range tmplRe.FindAllStringSubmatch(s, -1) {
		key := m[1]
		if _, ok := vars[key]; !ok && !seen[key] {
			seen[key] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// BuildVars собирает финальную map[key]string подставляя:
//   - встроенные: lampac_root, lampac_user
//   - значения из cfg (user-provided через UI)
//   - дефолты из manifest.params (для не заполненных юзером ключей)
func BuildVars(m *Manifest, cfg map[string]any, lampacRoot, lampacUser string) map[string]string {
	vars := map[string]string{
		"lampac_root": strings.TrimRight(lampacRoot, "/"),
		"lampac_user": lampacUser,
	}
	// Default-ы из manifest.params.
	for _, p := range m.Params {
		if p.Default == nil {
			continue
		}
		vars[p.Key] = anyToString(p.Default)
	}
	// User-overrides поверх.
	for k, v := range cfg {
		if v == nil {
			continue
		}
		vars[k] = anyToString(v)
	}
	return vars
}

func anyToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		// JSON numbers всегда float64. Если целое — без точки.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// ValidateConfig проверяет что config удовлетворяет schema из manifest.params.
// Возвращает map очищенных значений (с дефолтами для отсутствующих) и список
// ошибок ("ключ X должен быть int", "ключ Y отсутствует").
func ValidateConfig(m *Manifest, cfg map[string]any) (map[string]any, []string) {
	out := map[string]any{}
	errs := []string{}
	for _, p := range m.Params {
		v, present := cfg[p.Key]
		if !present || v == nil {
			if p.Default != nil {
				out[p.Key] = p.Default
			} else if p.Required {
				errs = append(errs, fmt.Sprintf("параметр %q обязателен", p.Key))
			}
			continue
		}
		switch strings.ToLower(p.Type) {
		case "", "string", "secret":
			s, ok := v.(string)
			if !ok {
				errs = append(errs, fmt.Sprintf("параметр %q должен быть строкой", p.Key))
				continue
			}
			out[p.Key] = s
		case "int":
			n, err := toInt(v)
			if err != nil {
				errs = append(errs, fmt.Sprintf("параметр %q должен быть числом: %v", p.Key, err))
				continue
			}
			if p.Min != nil && n < *p.Min {
				errs = append(errs, fmt.Sprintf("параметр %q < %d", p.Key, *p.Min))
				continue
			}
			if p.Max != nil && n > *p.Max {
				errs = append(errs, fmt.Sprintf("параметр %q > %d", p.Key, *p.Max))
				continue
			}
			out[p.Key] = n
		case "bool":
			b, ok := v.(bool)
			if !ok {
				if s, ok := v.(string); ok {
					b = s == "true" || s == "1" || s == "yes"
				} else {
					errs = append(errs, fmt.Sprintf("параметр %q должен быть true/false", p.Key))
					continue
				}
			}
			out[p.Key] = b
		case "enum":
			s, ok := v.(string)
			if !ok {
				errs = append(errs, fmt.Sprintf("параметр %q должен быть строкой из options", p.Key))
				continue
			}
			matched := false
			for _, opt := range p.Options {
				if opt == s {
					matched = true
					break
				}
			}
			if !matched {
				errs = append(errs, fmt.Sprintf("параметр %q = %q, ожидается один из %v", p.Key, s, p.Options))
				continue
			}
			out[p.Key] = s
		default:
			errs = append(errs, fmt.Sprintf("параметр %q имеет неизвестный type %q", p.Key, p.Type))
		}
	}
	return out, errs
}

func toInt(v any) (int, error) {
	switch t := v.(type) {
	case int:
		return t, nil
	case int64:
		return int(t), nil
	case float64:
		return int(t), nil
	case string:
		return strconv.Atoi(t)
	}
	return 0, fmt.Errorf("not a number: %T", v)
}
