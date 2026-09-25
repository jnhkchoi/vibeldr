// color_conv.go - conversions between Color and the standard library's colour
// types.
//
// color_conv.go - Color 와 표준 라이브러리 색 타입 사이의 변환.
package fbui

import "image/color"

// nrgba is the opaque colour handed to the font renderer.
// nrgba - 폰트 렌더러에 넘길 불투명 색.
func nrgba(c Color) color.RGBA {
	return color.RGBA{R: c.R, G: c.G, B: c.B, A: 255}
}
