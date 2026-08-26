// Package captcha 图片验证码生成与校验。
// 参考 Headscale-Admin-Pro:4 位字符集 234689(去易混字符 O/0/I/1),
// PNG 输出;本实现为纯 Go(标准库 image/png),不引 PIL/captcha 依赖。
// 一次性有效:校验通过即删除;TTL 到期自动清理。
package captcha

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math/big"
	"strconv"
	"sync"
	"time"
)

const (
	alphabet = "234689"
	length   = 4
	width    = 140
	height   = 50
	ttl      = 5 * time.Minute
	maxItems = 1000
)

// Store 验证码内存存储。id → entry,并发安全。
type Store struct {
	mu    sync.Mutex
	items map[string]entry
}

type entry struct {
	code   string
	expiry time.Time
}

// New 创建验证码存储。
func New() *Store {
	return &Store{items: make(map[string]entry)}
}

// Generate 生成新验证码,返回 id(客户端携带)与 PNG 图片字节。
func (s *Store) Generate() (id string, pngBytes []byte, err error) {
	code := randomCode()
	img := render(code)
	var buf bytes.Buffer
	if err = png.Encode(&buf, img); err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	// 防内存膨胀:超限清理过期项。
	if len(s.items) >= maxItems {
		s.evict()
	}
	id = randomID()
	s.items[id] = entry{code: code, expiry: time.Now().Add(ttl)}
	s.mu.Unlock()
	return id, buf.Bytes(), nil
}

// Validate 校验验证码。成功(匹配且未过期)返回 true 并删除(一次有效)。
func (s *Store) Validate(id, input string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[id]
	if !ok {
		return false
	}
	delete(s.items, id) // 无论对错,一次有效
	if time.Now().After(e.expiry) {
		return false
	}
	return e.code == input
}

// Cleanup 清理过期项,供周期调用。
func (s *Store) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evict()
}

func (s *Store) evict() {
	now := time.Now()
	for id, e := range s.items {
		if now.After(e.expiry) {
			delete(s.items, id)
		}
	}
}

func randomCode() string {
	b := make([]byte, length)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 失败用时间戳兜底(仅 id 可读性,非安全关键)。
		return "cap-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b)
}

// render 用标准库绘制验证码图:噪点背景 + 干扰线 + 字符。
func render(code string) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{0xF0, 0xF0, 0xF4, 0xFF}}, image.Point{}, draw.Src)

	// 噪点。
	for i := 0; i < 400; i++ {
		x, _ := rand.Int(rand.Reader, big.NewInt(width))
		y, _ := rand.Int(rand.Reader, big.NewInt(height))
		c := color.RGBA{uint8(randn(200)), uint8(randn(200)), uint8(randn(200)), 0xFF}
		img.Set(int(x.Int64()), int(y.Int64()), c)
	}
	// 干扰线。
	for i := 0; i < 3; i++ {
		x1, _ := rand.Int(rand.Reader, big.NewInt(width))
		y1, _ := rand.Int(rand.Reader, big.NewInt(height))
		x2, _ := rand.Int(rand.Reader, big.NewInt(width))
		y2, _ := rand.Int(rand.Reader, big.NewInt(height))
		drawLine(img, int(x1.Int64()), int(y1.Int64()), int(x2.Int64()), int(y2.Int64()),
			color.RGBA{uint8(randn(160)), uint8(randn(160)), uint8(randn(160)), 0xFF})
	}
	// 字符(粗伪块,足够人眼识别)。
	cell := width / length
	for i, ch := range code {
		x := i*cell + cell/3
		y := height/2 - 6
		drawDigit(img, x, y, ch, color.RGBA{0x1a, 0x1a, 0x2e, 0xFF})
	}
	return img
}

func drawDigit(img *image.RGBA, x, y int, ch rune, col color.Color) {
	// 7x9 位图字形,字符映射为 234689。
	font := map[rune][9]uint8{
		'2': {0b0111110, 0b1000001, 0b0000010, 0b0000100, 0b0011000, 0b0100000, 0b1000000, 0b1111111, 0b0000000},
		'3': {0b1111111, 0b0000100, 0b0001000, 0b0011100, 0b0000010, 0b0000001, 0b0000001, 0b1111110, 0b0000000},
		'4': {0b0000010, 0b0000110, 0b0001010, 0b0010010, 0b0100010, 0b1111111, 0b0000010, 0b0000010, 0b0000000},
		'6': {0b0011110, 0b0100000, 0b1000000, 0b1011110, 0b1100001, 0b1000001, 0b1000010, 0b0111100, 0b0000000},
		'8': {0b0111110, 0b1000001, 0b1000001, 0b0111110, 0b1000001, 0b1000001, 0b1000001, 0b0111110, 0b0000000},
		'9': {0b0111100, 0b1000010, 0b1000001, 0b1000001, 0b0111101, 0b0000010, 0b0000100, 0b0111000, 0b0000000},
	}
	rows, ok := font[ch]
	if !ok {
		return
	}
	for r := 0; r < 9; r++ {
		for bit := 0; bit < 7; bit++ {
			if rows[r]&(1<<(6-bit)) != 0 {
				img.Set(x+bit, y+r, col)
			}
		}
	}
}

func drawLine(img *image.RGBA, x1, y1, x2, y2 int, c color.Color) {
	dx := abs(x2 - x1)
	dy := abs(y2 - y1)
	sx, sy := 1, 1
	if x1 > x2 {
		sx = -1
	}
	if y1 > y2 {
		sy = -1
	}
	err := dx - dy
	for {
		img.Set(x1, y1, c)
		if x1 == x2 && y1 == y2 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x1 += sx
		}
		if e2 < dx {
			err += dx
			y1 += sy
		}
	}
}

func randn(max int) int {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(max)))
	return int(n.Int64())
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
