package modem

import (
	"sort"
	"strings"
)

// parseWindowsMBNInterfaceNames extracts the interface names emitted by
// `netsh mbn show interfaces`. netsh is localized, so accept the English
// labels as well as the common localized forms and do not depend on the
// surrounding status text.
func parseWindowsMBNInterfaceNames(output string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, 2)
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(rawLine, "\ufeff"))
		key, value, ok := strings.Cut(line, ":")
		if !ok || !windowsMBNNameKey(key) {
			continue
		}
		value = strings.TrimSpace(strings.Trim(value, "\"'"))
		if value == "" {
			continue
		}
		folded := strings.ToLower(value)
		if _, ok := seen[folded]; ok {
			continue
		}
		seen[folded] = struct{}{}
		result = append(result, value)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return strings.ToLower(result[i]) < strings.ToLower(result[j])
	})
	return result
}

func windowsMBNNameKey(value string) bool {
	value = strings.ToLower(strings.TrimSpace(strings.Trim(value, "\"'")))
	value = strings.Join(strings.Fields(value), " ")
	switch value {
	case "name", "interface name", "名称", "接口名称", "接口 名称":
		return true
	default:
		return false
	}
}
