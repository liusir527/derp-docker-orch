package derp

import (
	"regexp"
	"testing"
	"time"
)

var namePattern = regexp.MustCompile(`^derp-\d{8}-[a-z0-9]{4}$`)

func TestGenerateNameFormat(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		name, err := GenerateName(now)
		if err != nil {
			t.Fatalf("GenerateName: %v", err)
		}
		if !namePattern.MatchString(name) {
			t.Fatalf("名称 %q 不符合格式", name)
		}
		// 日期段与输入日期一致。
		if name[5:13] != "20260812" {
			t.Fatalf("名称 %q 日期段错误", name)
		}
	}
}

func TestGenerateNameUniqueness(t *testing.T) {
	now := time.Now()
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		name, err := GenerateName(now)
		if err != nil {
			t.Fatalf("GenerateName: %v", err)
		}
		if seen[name] {
			t.Fatalf("重复名称: %s", name)
		}
		seen[name] = true
	}
}
