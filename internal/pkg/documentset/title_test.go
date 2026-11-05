package documentset

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDisplayTitle(t *testing.T) {
	opaque := "set-021eed33daea96cca6e3eb16"
	for _, tc := range []struct {
		name, preferred, profile, want string
		inputs                         map[string]string
	}{
		{"explicit", "自定义名称", "工程设计", "自定义名称", map[string]string{"project_name": "项目"}},
		{"generic profile", "工程设计", "工程设计", "订单平台", map[string]string{"project_name": "订单平台", "goal": "恢复订单"}},
		{"subject", "", "工程设计", "订单设计", map[string]string{"subject": "  订单设计\n "}},
		{"goal", opaque, "工程设计", "恢复订单", map[string]string{"title": opaque, "goal": "恢复订单"}},
		{"profile fallback", "", "工程设计", "工程设计", nil},
		{"empty fallback", opaque, opaque, "文档成果集", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayTitle(SharedDesign{Inputs: tc.inputs}, tc.preferred, tc.profile); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	got := DisplayTitle(SharedDesign{Inputs: map[string]string{"goal": strings.Repeat("设计", 40)}}, "", "工程设计")
	if !utf8.ValidString(got) || len([]rune(got)) != 61 || !strings.HasSuffix(got, "…") {
		t.Fatalf("unsafe truncation: %q", got)
	}
	if got := DisplayTitle(SharedDesign{Facts: []Fact{{Kind: "requirement", Name: "订单恢复设计"}}}, "", "工程设计"); got != "订单恢复设计" {
		t.Fatal("requirement subject lost", got)
	}
}
