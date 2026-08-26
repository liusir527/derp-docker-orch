// Package redeem 兑换码生成与时长计算。
// 时长单位 day/month/year;月/年采用月末钳制(参考 Headscale-Admin-Pro redeem_service.add_months)。
package redeem

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// 默认前缀与长度上限(参考 HAP CODE_PREFIX 清洗规则)。
const (
	DefaultPrefix = "DERP"
	MaxPrefixLen  = 8
	codeSections  = 2 // XXXX-XXXX
	sectionLen    = 4

	MaxDurationDays   = 3650
	MaxDurationMonths = 120
	MaxDurationYears  = 10
)

var codeAlphabet = []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

// NormalizePrefix 清洗前缀:仅保留字母数字,大写,去空白;超长截断;空则用默认。
func NormalizePrefix(prefix string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(prefix)) {
		if r == ' ' || r == '-' || r == '_' {
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			continue
		}
		if b.Len() >= MaxPrefixLen {
			break
		}
		b.WriteRune(r)
		if b.Len() >= MaxPrefixLen {
			break
		}
	}
	if b.Len() == 0 {
		return DefaultPrefix
	}
	return b.String()
}

// NormalizeCode 归一化兑换码:去空白、大写。
func NormalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// GenerateCode 生成 <PREFIX>-XXXX-XXXX 格式兑换码。
func GenerateCode(prefix string) (string, error) {
	var b [codeSections * sectionLen]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成兑换码随机段: %w", err)
	}
	var sb strings.Builder
	sb.WriteString(NormalizePrefix(prefix))
	sb.WriteByte('-')
	for i := range b {
		if i > 0 && i%sectionLen == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(codeAlphabet[int(b[i])%len(codeAlphabet)])
	}
	return sb.String(), nil
}

// ValidateDuration 校验时长数值与单位,返回规范化值。错误信息面向用户。
func ValidateDuration(value int, unit string) (int, string, error) {
	switch unit {
	case "day":
		if value <= 0 || value > MaxDurationDays {
			return 0, "", fmt.Errorf("天数必须在 1-%d 之间", MaxDurationDays)
		}
	case "month":
		if value <= 0 || value > MaxDurationMonths {
			return 0, "", fmt.Errorf("月数必须在 1-%d 之间", MaxDurationMonths)
		}
	case "year":
		if value <= 0 || value > MaxDurationYears {
			return 0, "", fmt.Errorf("年数必须在 1-%d 之间", MaxDurationYears)
		}
	default:
		return 0, "", fmt.Errorf("时长单位必须是 day/month/year")
	}
	return value, unit, nil
}

// ExpiryFrom 从基准时间计算过期时间。月/年按自然月(月末钳制)。
func ExpiryFrom(base time.Time, value int, unit string) (time.Time, error) {
	v, u, err := ValidateDuration(value, unit)
	if err != nil {
		return time.Time{}, err
	}
	switch u {
	case "day":
		return base.AddDate(0, 0, v), nil
	case "month":
		return addMonths(base, v), nil
	case "year":
		return addMonths(base, v*12), nil
	}
	return time.Time{}, fmt.Errorf("未知单位 %q", unit)
}

// addMonths 加 N 个月,月末钳制(1月31日+1月=2月28/29日)。
func addMonths(t time.Time, months int) time.Time {
	monthIndex := int(t.Month()) - 1 + months
	year := t.Year() + monthIndex/12
	month := monthIndex%12 + 1
	lastDay := daysInMonth(year, month)
	day := t.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, time.Month(month), day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

func daysInMonth(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
