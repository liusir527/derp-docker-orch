package captcha

import (
	"bytes"
	"image/png"
	"sync"
	"testing"
)

func TestGenerate_ValidPNG(t *testing.T) {
	s := New()
	id, data, err := s.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if id == "" || len(data) == 0 {
		t.Fatal("id 或图片为空")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("解码 PNG: %v", err)
	}
	if img.Bounds().Dx() != width || img.Bounds().Dy() != height {
		t.Errorf("尺寸不符: %v", img.Bounds())
	}
}

func TestValidate_OnceAndWrong(t *testing.T) {
	s := New()
	id, _, _ := s.Generate()
	// 无法知道 code;测:任意输入错误 → false 且已消耗。
	if s.Validate(id, "xxxx") {
		t.Fatal("错误输入应拒绝")
	}
	// 再次校验(已被删除)→ false(一次有效)。
	if s.Validate(id, "xxxx") {
		t.Fatal("已消耗的验证码应拒绝")
	}
}

func TestValidate_Replay(t *testing.T) {
	s := New()
	id, _, _ := s.Generate()
	// 直接注入已知 code 模拟正确匹配路径。
	s.mu.Lock()
	e := s.items[id]
	s.mu.Unlock()
	if !s.Validate(id, e.code) {
		t.Fatal("正确输入应通过")
	}
	if s.Validate(id, e.code) {
		t.Fatal("重放应拒绝")
	}
}

func TestConcurrentGenerate(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.Generate()
			if err != nil {
				t.Errorf("并发 Generate: %v", err)
			}
		}()
	}
	wg.Wait()
}
