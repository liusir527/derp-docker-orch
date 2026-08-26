package redeem

import (
	"regexp"
	"testing"
	"time"
)

func TestGenerateCode_FormatAndUniqueness(t *testing.T) {
	re := regexp.MustCompile(`^DERP-[A-Z0-9]{4}-[A-Z0-9]{4}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := GenerateCode("")
		if err != nil {
			t.Fatalf("GenerateCode: %v", err)
		}
		if !re.MatchString(c) {
			t.Fatalf("格式不符: %q", c)
		}
		if seen[c] {
			t.Fatalf("重复码: %s", c)
		}
		seen[c] = true
	}
}

func TestGenerateCode_CustomPrefix(t *testing.T) {
	re := regexp.MustCompile(`^VIP-[A-Z0-9]{4}-[A-Z0-9]{4}$`)
	c, err := GenerateCode("vip")
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if !re.MatchString(c) {
		t.Fatalf("自定义前缀格式不符: %q", c)
	}
}

func TestNormalizePrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "DERP"},
		{"  vip  ", "VIP"},
		{"my-code_1", "MYCODE1"},
		{"hello world!!@#", "HELLOWOR"}, // 10 位 → 截断 8
		{"ABCDEFGHIJKLMN", "ABCDEFGH"}, // 超长截断 8 位
		{"123", "123"},
	}
	for _, c := range cases {
		if got := NormalizePrefix(c.in); got != c.want {
			t.Errorf("NormalizePrefix(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestValidateDuration(t *testing.T) {
	bad := []struct {
		value int
		unit  string
	}{
		{0, "day"}, {3651, "day"}, {-1, "month"}, {121, "month"},
		{0, "year"}, {11, "year"}, {1, "week"},
	}
	for _, b := range bad {
		if _, _, err := ValidateDuration(b.value, b.unit); err == nil {
			t.Errorf("期望校验失败: %+v", b)
		}
	}
	if _, _, err := ValidateDuration(30, "day"); err != nil {
		t.Errorf("合法值被拒: %v", err)
	}
	if _, _, err := ValidateDuration(3, "year"); err != nil {
		t.Errorf("合法值被拒: %v", err)
	}
}

func TestExpiryFrom_MonthEndClamp(t *testing.T) {
	// 2026-01-31 +1 月 = 2026-02-28(月末钳制,2026 非闰年)。
	base := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
	got, err := ExpiryFrom(base, 1, "month")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("月末钳制失败: got %v, want %v", got, want)
	}

	// 2024-01-31 +1 月 = 2024-02-29(闰年)。
	leap := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)
	got2, _ := ExpiryFrom(leap, 1, "month")
	if want2 := time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC); !got2.Equal(want2) {
		t.Errorf("闰年钳制失败: got %v, want %v", got2, want2)
	}

	// 年 = 12 个月。
	y, _ := ExpiryFrom(base, 1, "year")
	if !y.Equal(time.Date(2027, 1, 31, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("年计算失败: %v", y)
	}

	// 天。
	d, _ := ExpiryFrom(base, 30, "day")
	if !d.Equal(time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("天计算失败: %v", d)
	}
}
