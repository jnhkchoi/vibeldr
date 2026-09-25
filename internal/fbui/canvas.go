// Package fbui is the graphical UI toolkit for the loader's install wizard.
//
// All rendering finishes in an RGBA buffer in memory (Canvas). Actually putting
// it on screen is the Display implementation's job, and where there is no
// framebuffer the result can be written out as a PNG and looked at. That is why
// the toolkit itself builds and tests on any OS.
//
// The coordinate system has its origin top left and counts in pixels. There is
// no alpha compositing, only opaque overwriting - the wizard screens are all
// opaque panels, so blending buys nothing and costs framebuffer bandwidth.
// Antialiased text is the one exception and is mixed with the background.
//
// Package fbui - 로더 설치 마법사의 그래픽 UI 툴킷.
//
// 렌더링은 전부 메모리 위의 RGBA 버퍼(Canvas) 에서 끝난다. 화면에 실제로
// 내보내는 일은 Display 구현이 맡고, 프레임버퍼가 없는 환경에서는 PNG 로
// 떠서 눈으로 확인할 수 있다. 그래서 툴킷 자체는 어느 OS 에서도 빌드되고
// 테스트된다.
//
// 좌표계는 좌상단 원점, 픽셀 단위. 알파 합성은 하지 않고 불투명 덮어쓰기만
// 한다 - 마법사 화면은 전부 불투명 패널이라 블렌딩이 필요 없고, 없는 편이
// 프레임버퍼 대역폭을 덜 쓴다. 안티에일리어싱된 글자만 예외적으로 배경색과
// 섞어서 그린다.
package fbui

import "image"

// Color is 8-bit RGB, converted to the framebuffer's pixel format on the way out.
// Color - 8 비트 RGB. 프레임버퍼로 나갈 때 해당 픽셀 포맷으로 변환된다.
type Color struct{ R, G, B uint8 }

// RGB makes a Color from a 0xRRGGBB integer, for writing a palette as constants.
// RGB - 0xRRGGBB 정수에서 Color 를 만든다. 팔레트를 상수로 적을 때 쓴다.
func RGB(v uint32) Color {
	return Color{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}
}

// Blend mixes src over dst by alpha (0-255). It is used for text antialiasing
// and for the shading on a pressed button.
//
// Blend - dst 위에 src 를 alpha(0~255) 만큼 섞은 색. 글자 안티에일리어싱과
// 눌린 버튼의 음영에 쓴다.
func Blend(dst, src Color, alpha uint8) Color {
	if alpha == 0 {
		return dst
	}
	if alpha == 255 {
		return src
	}
	a := uint32(alpha)
	ia := 255 - a
	return Color{
		R: uint8((uint32(src.R)*a + uint32(dst.R)*ia) / 255),
		G: uint8((uint32(src.G)*a + uint32(dst.G)*ia) / 255),
		B: uint8((uint32(src.B)*a + uint32(dst.B)*ia) / 255),
	}
}

// Rect is a rectangle, top-left inclusive and bottom-right exclusive.
// Rect - 좌상단 포함, 우하단 제외의 사각형.
type Rect struct{ X, Y, W, H int }

// Contains reports whether a point is inside the rectangle, for mouse hit tests.
// Contains - 점이 사각형 안에 있는지. 마우스 히트 테스트용.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Inset is the rectangle shrunk by d pixels on every side, for filling inside a border.
// Inset - 사방으로 d 픽셀 줄인 사각형. 테두리 안쪽을 칠할 때.
func (r Rect) Inset(d int) Rect {
	return Rect{X: r.X + d, Y: r.Y + d, W: r.W - 2*d, H: r.H - 2*d}
}

// Canvas is a pixel buffer.
//
// It stores pixels in the same layout as image.RGBA (four bytes of R, G, B, A
// per pixel) and shares the buffer with such an image. That lets the glyph
// renderer use the standard font API directly, and the conversion to the
// framebuffer's pixel format happens only on the way to the screen. Alpha is
// always kept at 255.
//
// Canvas - 픽셀 버퍼.
//
// 내부 저장은 image.RGBA 와 같은 레이아웃(픽셀당 R,G,B,A 4 바이트) 이고
// 버퍼를 그 이미지와 공유한다. 덕분에 글리프 렌더러가 표준 폰트 API 를
// 그대로 쓸 수 있고, 화면으로 내보낼 때만 프레임버퍼의 픽셀 포맷으로
// 바꾸면 된다. 알파는 항상 255 로 유지한다.
type Canvas struct {
	Pix    []uint8
	Stride int
	W, H   int
	// img is the standard image view onto the same memory as Pix.
	// img - Pix 와 같은 메모리를 가리키는 표준 이미지 뷰.
	img *image.RGBA
	// clip is where drawing is currently allowed. It is here to cut off the
	// pixels that spill out when a scrolling list is drawn inside a panel.
	//
	// clip - 현재 그리기가 허용된 영역. 패널 안에 스크롤 목록을 그릴 때
	// 넘치는 픽셀을 잘라내려고 둔다.
	clip Rect
}

// NewCanvas is an empty w by h canvas, opaque black throughout to begin with.
// NewCanvas - w x h 크기의 빈 캔버스. 전체가 불투명 검정으로 시작한다.
func NewCanvas(w, h int) *Canvas {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return &Canvas{
		Pix:    img.Pix,
		Stride: img.Stride,
		W:      w, H: h,
		img:  img,
		clip: Rect{0, 0, w, h},
	}
}

// Image is the standard image view sharing memory with Pix, used by the font
// renderer and the PNG encoder. Drawing into it draws into the canvas.
//
// Image - Pix 와 메모리를 공유하는 표준 이미지 뷰. 폰트 렌더러와 PNG
// 인코더가 쓴다. 여기에 그리면 캔버스에 그대로 반영된다.
func (c *Canvas) Image() *image.RGBA { return c.img }

// Clip narrows the drawing area and returns the previous one. The caller has to
// put that value back when it is done.
//
// Clip - 그리기 영역을 제한하고 이전 영역을 돌려준다. 호출자는 끝나고
// 돌려받은 값으로 되돌려야 한다.
func (c *Canvas) Clip(r Rect) Rect {
	old := c.clip
	c.clip = intersect(old, r)
	return old
}

// SetClip restores what Clip returned.
// SetClip - Clip 이 돌려준 값으로 되돌린다.
func (c *Canvas) SetClip(r Rect) { c.clip = r }

// intersect is the overlap of two rectangles, never negative in size.
// intersect - 사각형 둘이 겹치는 부분. 크기가 음수가 되지 않는다.
func intersect(a, b Rect) Rect {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	if x1 < x0 {
		x1 = x0
	}
	if y1 < y0 {
		y1 = y0
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

// Set writes one pixel, ignoring anything outside the clip without comment.
// Set - 픽셀 하나. 클립 밖은 조용히 무시한다.
func (c *Canvas) Set(x, y int, col Color) {
	if !c.clip.Contains(x, y) {
		return
	}
	i := y*c.Stride + x*4
	c.Pix[i] = col.R
	c.Pix[i+1] = col.G
	c.Pix[i+2] = col.B
	c.Pix[i+3] = 255
}

// At reads one pixel, which text antialiasing needs in order to know the
// background colour. It ignores the clip; outside the canvas it returns black.
//
// At - 픽셀 하나 읽기. 글자 안티에일리어싱이 배경색을 알아야 해서 필요하다.
// 클립은 보지 않고, 캔버스 밖이면 검정을 돌려준다.
func (c *Canvas) At(x, y int) Color {
	if x < 0 || y < 0 || x >= c.W || y >= c.H {
		return Color{}
	}
	i := y*c.Stride + x*4
	return Color{R: c.Pix[i], G: c.Pix[i+1], B: c.Pix[i+2]}
}

// Fill paints a rectangle in one colour. One row is built and then copied, so
// the bounds check does not repeat per pixel.
//
// Fill - 사각형을 단색으로 채운다. 한 행을 만들어 두고 복사해서, 픽셀마다
// 경계 검사를 반복하지 않는다.
func (c *Canvas) Fill(r Rect, col Color) {
	r = intersect(r, c.clip)
	if r.W <= 0 || r.H <= 0 {
		return
	}
	row := make([]uint8, r.W*4)
	for x := 0; x < r.W; x++ {
		row[x*4] = col.R
		row[x*4+1] = col.G
		row[x*4+2] = col.B
		row[x*4+3] = 255
	}
	for y := r.Y; y < r.Y+r.H; y++ {
		copy(c.Pix[y*c.Stride+r.X*4:], row)
	}
}

// Border draws a rectangle's border t pixels thick, inwards.
// Border - 사각형 테두리를 두께 t 로 그린다 (안쪽으로).
func (c *Canvas) Border(r Rect, t int, col Color) {
	if t <= 0 {
		return
	}
	c.Fill(Rect{r.X, r.Y, r.W, t}, col)
	c.Fill(Rect{r.X, r.Y + r.H - t, r.W, t}, col)
	c.Fill(Rect{r.X, r.Y + t, t, r.H - 2*t}, col)
	c.Fill(Rect{r.X + r.W - t, r.Y + t, t, r.H - 2*t}, col)
}

// HLine is a horizontal rule.
// HLine - 가로 구분선.
func (c *Canvas) HLine(x, y, w int, col Color) { c.Fill(Rect{x, y, w, 1}, col) }

// RGBA is the standard image view; it returns the same thing as Image.
// RGBA - 표준 이미지 뷰. Image 와 같은 것을 돌려준다.
func (c *Canvas) RGBA() *image.RGBA { return c.img }
