// render_test.go writes the rendering out as PNGs so the result can be looked
// at.
//
// The screen has to be inspectable on a development machine with no
// framebuffer, so it draws onto a canvas and saves under testdata. It also
// checks values - whether the text really left pixels behind - so a regression
// is caught without opening the image.
//
// render_test.go - 렌더링 결과를 눈으로 확인할 수 있게 PNG 로 떠 두는 테스트.
//
// 프레임버퍼가 없는 개발 환경에서도 화면을 검사할 수 있어야 해서, 캔버스에
// 그린 뒤 testdata 아래로 저장한다. 값 검사(글자가 실제로 픽셀을 남겼는지)
// 도 함께 해서, 그림 파일을 열어보지 않아도 회귀를 잡는다.
package fbui

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// dumpPNG saves a canvas under testdata.
// dumpPNG - 캔버스를 testdata 에 저장한다.
func dumpPNG(t *testing.T, c *Canvas, name string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, c.RGBA()); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}

// countNonBackground is how many pixels differ from the background colour, used
// to decide whether the text was actually drawn.
//
// countNonBackground - 배경색과 다른 픽셀 수. 글자가 실제로 그려졌는지
// 판정하는 데 쓴다.
func countNonBackground(c *Canvas, bg Color) int {
	n := 0
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			if c.At(x, y) != bg {
				n++
			}
		}
	}
	return n
}

// TestTextRendersHangul: whether Korean leaves pixels behind. If the embedded
// font lacks the glyphs, nothing is drawn and this test catches it.
//
// TestTextRendersHangul - 한글이 픽셀을 남기는지 본다. 임베드 폰트에 해당
// 글리프가 없으면 아무것도 안 그려지고 이 테스트가 잡는다.
func TestTextRendersHangul(t *testing.T) {
	bg := RGB(0x1E3A5F)
	c := NewCanvas(640, 200)
	c.Fill(Rect{0, 0, 640, 200}, bg)

	face, err := LoadFace(22)
	if err != nil {
		t.Fatal(err)
	}
	c.Text(20, 60, "모델 선택: DS918+", RGB(0xFFFFFF), face)
	c.Text(20, 100, "시리얼 번호를 자동 생성합니다", RGB(0xC8DCF0), face)
	c.Text(20, 140, "SATA controller 0000:00:1f.2 - 6 ports", RGB(0xC8DCF0), face)

	dumpPNG(t, c, "text.png")

	if n := countNonBackground(c, bg); n < 2000 {
		t.Errorf("글자가 거의 안 그려짐: 배경과 다른 픽셀 %d 개", n)
	}
}

// TestMeasureAndTruncate: whether the width calculation and the ellipsis are
// right. A string wider than the panel must not push the layout out of shape.
//
// TestMeasureAndTruncate - 폭 계산과 줄임표가 맞는지 본다. 패널 폭을 넘는
// 문자열이 레이아웃을 밀어내면 안 된다.
func TestMeasureAndTruncate(t *testing.T) {
	face, err := LoadFace(18)
	if err != nil {
		t.Fatal(err)
	}
	if w := face.Measure("DS3622xs+"); w <= 0 {
		t.Fatalf("폭이 %d", w)
	}
	long := "QEMU HARDDISK 매우 긴 모델명 0123456789"
	full := face.Measure(long)
	cut := face.Truncate(long, full/2)
	if cut == long {
		t.Error("잘려야 하는데 원본 그대로")
	}
	if w := face.Measure(cut); w > full/2 {
		t.Errorf("자른 문자열이 여전히 넓음: %d > %d", w, full/2)
	}
	if face.Truncate("짧음", 1000) != "짧음" {
		t.Error("들어가는 문자열을 자르면 안 됨")
	}
}

// TestClipConfinesText: whether any text leaks outside the clip.
// TestClipConfinesText - 클립 밖으로 글자가 새지 않는지 본다.
func TestClipConfinesText(t *testing.T) {
	bg := Color{}
	c := NewCanvas(300, 100)
	c.Fill(Rect{0, 0, 300, 100}, bg)
	face := MustFace(20)

	old := c.Clip(Rect{0, 0, 100, 100})
	c.Text(10, 50, "가나다라마바사아자차카타파하", RGB(0xFFFFFF), face)
	c.SetClip(old)

	for y := 0; y < 100; y++ {
		for x := 100; x < 300; x++ {
			if c.At(x, y) != bg {
				t.Fatalf("클립 밖 (%d,%d) 에 픽셀이 그려짐", x, y)
			}
		}
	}
}
