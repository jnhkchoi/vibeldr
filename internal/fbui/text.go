// text.go renders text.
//
// Every character is drawn from one embedded TrueType font. Korean has to work,
// and the boot environment has no fonts installed, so the binary carries one
// itself. A Face per size is built once and cached - rebuilding the glyph
// rasteriser on every frame would be slow.
//
// text.go - 글자 렌더링.
//
// 임베드한 TrueType 폰트 하나로 모든 글자를 그린다. 한글이 필요하고,
// 부팅 환경에는 폰트가 설치돼 있지 않으므로 바이너리가 직접 들고 간다.
// 크기별 Face 는 한 번 만들어 캐시한다 - 글리프 래스터라이저를 다시
// 세우는 비용이 프레임마다 반복되면 느려진다.
package fbui

import (
	_ "embed"
	"fmt"
	"image"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// nanumGothic is the embedded font's bytes: Nanum Gothic, distributed under the
// SIL Open Font License 1.1, whose full text is in NanumGothic-OFL.txt in the
// same directory.
//
// nanumGothic - 임베드 폰트 바이트. SIL Open Font License 1.1 로 배포되는
// 나눔고딕이며 라이선스 전문은 같은 디렉터리의 NanumGothic-OFL.txt 에 있다.
//
//go:embed assets/NanumGothic-Regular.ttf
var nanumGothic []byte

// FontLicense is the embedded font's full licence text, carried in the binary
// alongside the font. No screen displays it.
//
// FontLicense - 임베드 폰트의 라이선스 전문. 폰트와 함께 바이너리에 담는다.
// 이것을 보여주는 화면은 없다.
//
//go:embed assets/NanumGothic-OFL.txt
var FontLicense string

var (
	fontOnce   sync.Once
	parsedFont *sfnt.Font
	fontErr    error

	faceMu    sync.Mutex
	faceCache = map[int]*Face{}
)

// Face is the font at one pixel size, used for measuring and for drawing.
// Face - 특정 픽셀 크기의 글꼴. 폭 계산과 그리기에 쓴다.
type Face struct {
	face font.Face
	// px is the pixel size asked for: the cache key, and what line spacing is
	// worked out from.
	//
	// px - 요청한 픽셀 크기. 캐시 키이자 줄 간격 계산의 기준.
	px int
	// ascent, descent and lineHeight are the font metrics rounded up to pixels.
	// ascent, descent, lineHeight - 폰트 메트릭을 픽셀로 올림한 값.
	ascent, descent, lineHeight int
}

// LoadFace gets the Face at px pixels. Asking for the same size again returns
// the cached one.
//
// LoadFace - px 픽셀 크기의 Face 를 얻는다. 같은 크기를 다시 요청하면
// 캐시된 것을 돌려준다.
func LoadFace(px int) (*Face, error) {
	if px <= 0 {
		return nil, fmt.Errorf("fbui: 글꼴 크기가 %d", px)
	}
	fontOnce.Do(func() { parsedFont, fontErr = opentype.Parse(nanumGothic) })
	if fontErr != nil {
		return nil, fmt.Errorf("fbui: 임베드 폰트 파싱: %w", fontErr)
	}

	faceMu.Lock()
	defer faceMu.Unlock()
	if f, ok := faceCache[px]; ok {
		return f, nil
	}
	// At DPI 72, Size is the em size in pixels directly.
	// DPI 72 로 두면 Size 가 그대로 픽셀 em 크기가 된다.
	ff, err := opentype.NewFace(parsedFont, &opentype.FaceOptions{
		Size: float64(px), DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("fbui: %dpx face: %w", px, err)
	}
	m := ff.Metrics()
	f := &Face{
		face:       ff,
		px:         px,
		ascent:     ceilFixed(m.Ascent),
		descent:    ceilFixed(m.Descent),
		lineHeight: ceilFixed(m.Height),
	}
	// A reported line height narrower than ascent+descent would overlap the
	// lines.
	//
	// 폰트가 보고한 줄 높이가 ascent+descent 보다 좁으면 글자가 겹친다.
	if f.lineHeight < f.ascent+f.descent {
		f.lineHeight = f.ascent + f.descent
	}
	faceCache[px] = f
	return f, nil
}

// MustFace is LoadFace that panics. The font is embedded at build time, so a
// failure here is a build mistake rather than a runtime condition. It is for
// initialising the UI constants.
//
// MustFace - LoadFace 의 패닉 버전. 임베드 폰트는 빌드에 포함되므로 실패는
// 빌드 실수이지 런타임 조건이 아니다. UI 상수를 초기화할 때 쓴다.
func MustFace(px int) *Face {
	f, err := LoadFace(px)
	if err != nil {
		panic(err)
	}
	return f
}

// Ascent is the height above the baseline, used to centre text vertically in a
// rectangle.
//
// Ascent - 베이스라인 위쪽 높이. 사각형 안에 세로 가운데 정렬할 때 쓴다.
func (f *Face) Ascent() int { return f.ascent }

// Height is how tall one line is.
// Height - 한 줄이 차지하는 높이.
func (f *Face) Height() int { return f.lineHeight }

// Measure is how wide a string would be in pixels.
// Measure - 문자열을 그렸을 때의 가로 픽셀 폭.
func (f *Face) Measure(s string) int {
	return ceilFixed(font.MeasureString(f.face, s))
}

// Truncate shortens a string to fit max pixels, adding an ellipsis if it had to
// cut. It keeps a value of unpredictable length, such as a disk model name,
// from pushing the panel out of shape.
//
// Truncate - 폭 max 픽셀에 들어가도록 문자열을 줄이고, 잘렸으면 끝에
// 줄임표를 붙인다. 디스크 모델명처럼 길이를 예측할 수 없는 값이 패널을
// 밀어내지 않게 한다.
func (f *Face) Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if f.Measure(s) <= max {
		return s
	}
	const ellipsis = "…"
	ew := f.Measure(ellipsis)
	if ew > max {
		return ""
	}
	r := []rune(s)
	for len(r) > 0 {
		r = r[:len(r)-1]
		if f.Measure(string(r))+ew <= max {
			return string(r) + ellipsis
		}
	}
	return ellipsis
}

// ceilFixed rounds a 26.6 fixed-point value up to whole pixels.
// ceilFixed - 26.6 고정소수점을 위로 올림한 정수 픽셀.
func ceilFixed(v fixed.Int26_6) int { return int((v + 0x3f) >> 6) }

// Text draws one line starting at (x, baselineY) and returns how wide what it
// drew is. It never goes outside the current clip.
//
// Text - (x, baselineY) 에서 시작하는 한 줄. 그린 글자의 가로 폭을 돌려준다.
//
// 현재 클립 영역 밖으로는 나가지 않는다.
func (c *Canvas) Text(x, baselineY int, s string, col Color, f *Face) int {
	if s == "" || f == nil {
		return 0
	}
	cl := c.clip
	if cl.W <= 0 || cl.H <= 0 {
		return 0
	}
	// Narrowing the standard image view to the clip keeps the rasteriser from
	// touching anything outside it. The coordinate system is unchanged.
	//
	// 표준 이미지 뷰를 클립 영역으로 제한하면 래스터라이저가 그 밖을
	// 건드리지 않는다. 좌표계는 그대로 유지된다.
	dst := c.img.SubImage(image.Rect(cl.X, cl.Y, cl.X+cl.W, cl.Y+cl.H)).(*image.RGBA)
	d := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(nrgba(col)),
		Face: f.face,
		Dot:  fixed.P(x, baselineY),
	}
	d.DrawString(s)
	return ceilFixed(d.Dot.X) - x
}

// TextIn draws one line aligned inside a rectangle: align is -1 for left, 0 for
// centre, 1 for right. Vertically it is always centred.
//
// TextIn - 사각형 안에 한 줄을 정렬해 그린다. align 은 -1 왼쪽, 0 가운데,
// 1 오른쪽. 세로는 항상 가운데.
func (c *Canvas) TextIn(r Rect, s string, col Color, f *Face, align int) {
	if s == "" || f == nil {
		return
	}
	w := f.Measure(s)
	x := r.X
	switch {
	case align == 0:
		x = r.X + (r.W-w)/2
	case align > 0:
		x = r.X + r.W - w
	}
	// Vertical centring: the middle of the rectangle, corrected by half of
	// (ascent - descent).
	//
	// 세로 가운데: 사각형 중앙에 (ascent - descent) 의 절반을 보정.
	baseline := r.Y + (r.H+f.ascent-f.descent)/2
	c.Text(x, baseline, s, col, f)
}
