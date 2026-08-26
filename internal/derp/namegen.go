// Package derp 提供实例命名与 derpMap 生成。
package derp

import (
	"crypto/rand"
	"fmt"
	"time"
)

const (
	// randAlphabet 名称随机段字符集。
	randAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	// randLen 随机段长度。
	randLen = 4
)

// GenerateName 生成 derp-<YYYYMMDD>-<rand4> 格式名称。
// 日期段保证"与日期有关",随机段保证同日内唯一;最终唯一性由 DB 约束兜底。
func GenerateName(now time.Time) (string, error) {
	var b [randLen]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成随机段: %w", err)
	}
	// 取模避免索引越界,字符集 36 非 2 的幂,轻微偏斜可接受(仅命名美观)。
	for i := range b {
		b[i] = randAlphabet[int(b[i])%len(randAlphabet)]
	}
	return fmt.Sprintf("derp-%s-%s", now.Format("20060102"), string(b[:])), nil
}
