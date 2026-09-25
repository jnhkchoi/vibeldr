// shapes.go draws rounded shapes and shadows.
//
// Square corners alone make the screen look crude. Buttons and cards get their
// corners cut, and the edge is mixed with the background to take the stairsteps
// out. The circle equation gives each row's start and end, and a pixel that
// straddles the edge is blended by how much of it is covered.
//
// shapes.go - 모서리가 둥근 도형과 그림자.
//
// 직각 사각형만으로는 화면이 투박해 보인다. 버튼과 카드의 모서리를 깎고
// 경계를 배경과 섞어 계단을 지운다. 원 방정식으로 각 행의 시작·끝을
// 구하고, 경계에 걸친 픽셀은 덮인 비율만큼 섞는다.
package fbui

import "math"

// FillRound fills a rectangle with rounded corners. A radius of 0 is Fill.
// FillRound - 모서리가 둥근 사각형을 채운다. radius 가 0 이면 Fill 과 같다.
func (c *Canvas) FillRound(r Rect, radius int, col Color) {
	if radius <= 0 {
		c.Fill(r, col)
		return
	}
	if radius*2 > r.W {
		radius = r.W / 2
	}
	if radius*2 > r.H {
		radius = r.H / 2
	}

	// The middle band is painted in one go; only the corner bands at the top
	// and bottom are worked out row by row.
	//
	// 가운데 띠는 통째로 칠하고, 위아래 모서리 구간만 행마다 계산한다.
	c.Fill(Rect{r.X, r.Y + radius, r.W, r.H - 2*radius}, col)

	for dy := 0; dy < radius; dy++ {
		// dy counts from the top (or bottom) edge of the rectangle. The
		// nearer the edge, the further the circle pulls back.
		//
		// dy 는 사각형의 위(아래) 끝에서부터 센 거리다. 끝에 가까울수록
		// 원이 많이 물러난다.
		inset, cover := arcInset(radius, dy)
		top := r.Y + dy
		bot := r.Y + r.H - 1 - dy

		c.Fill(Rect{r.X + inset, top, r.W - 2*inset, 1}, col)
		c.Fill(Rect{r.X + inset, bot, r.W - 2*inset, 1}, col)

		// The one pixel straddling the edge is blended by its coverage.
		// 경계에 걸친 한 픽셀은 덮인 만큼만 섞는다.
		if inset > 0 && cover > 0 {
			a := uint8(cover * 255)
			for _, p := range [][2]int{
				{r.X + inset - 1, top}, {r.X + r.W - inset, top},
				{r.X + inset - 1, bot}, {r.X + r.W - inset, bot},
			} {
				c.Set(p[0], p[1], Blend(c.At(p[0], p[1]), col, a))
			}
		}
	}
}

// arcInset gives, for a circle of the given radius, how many pixels the row dy
// away from the corner pulls back along x, and how much of the pixel just
// outside that is covered.
//
// arcInset - 반지름 radius 인 원에서, 모서리 끝으로부터 dy 만큼 떨어진
// 행이 x 축으로 물러나는 픽셀 수와, 그 바로 바깥 픽셀이 덮인 비율.
func arcInset(radius, dy int) (int, float64) {
	// The circle's centre is (radius, radius); this is the vertical distance
	// to the middle of this row.
	//
	// 원의 중심은 (radius, radius). 이 행의 중심까지 세로 거리.
	y := float64(radius) - float64(dy) - 0.5
	x := math.Sqrt(float64(radius*radius) - y*y)
	full := float64(radius) - x
	inset := int(full)
	return inset, full - float64(inset)
}

// BorderRound draws a rounded border. It does not clear the inside, so it goes
// on after the fill.
//
// BorderRound - 모서리가 둥근 테두리. 안쪽을 지우지 않으므로 채운 뒤에
// 그린다.
func (c *Canvas) BorderRound(r Rect, radius, t int, col Color) {
	if t <= 0 {
		return
	}
	if radius <= 0 {
		c.Border(r, t, col)
		return
	}
	if radius*2 > r.W {
		radius = r.W / 2
	}
	if radius*2 > r.H {
		radius = r.H / 2
	}

	// The straight sides.
	// 곧은 변.
	c.Fill(Rect{r.X + radius, r.Y, r.W - 2*radius, t}, col)
	c.Fill(Rect{r.X + radius, r.Y + r.H - t, r.W - 2*radius, t}, col)
	c.Fill(Rect{r.X, r.Y + radius, t, r.H - 2*radius}, col)
	c.Fill(Rect{r.X + r.W - t, r.Y + radius, t, r.H - 2*radius}, col)

	// The corner arcs: paint between the outer and the inner radius.
	// 모서리 호. 바깥 반지름과 안쪽 반지름 사이를 칠한다.
	for dy := 0; dy < radius; dy++ {
		outer, cover := arcInset(radius, dy)
		inner, _ := arcInset(radius-t, dy-t)
		if dy < t {
			inner = 0
		}
		w := inner - outer
		if w < t {
			w = t
		}
		top := r.Y + dy
		bot := r.Y + r.H - 1 - dy
		for _, y := range []int{top, bot} {
			c.Fill(Rect{r.X + outer, y, w, 1}, col)
			c.Fill(Rect{r.X + r.W - outer - w, y, w, 1}, col)
			if outer > 0 && cover > 0 {
				a := uint8(cover * 255)
				for _, x := range []int{r.X + outer - 1, r.X + r.W - outer} {
					c.Set(x, y, Blend(c.At(x, y), col, a))
				}
			}
		}
	}
}

// Circle is a filled circle, used for the badge carrying a step number.
// Circle - 채운 원. 단계 번호를 담는 배지에 쓴다.
func (c *Canvas) Circle(cx, cy, radius int, col Color) {
	c.FillRound(Rect{cx - radius, cy - radius, radius * 2, radius * 2}, radius, col)
}

// Shadow lays a faint shadow under a rectangle. It is only there to lift a card
// off the background, so three thin layers, each one pixel further out and
// fainter than the last, are enough.
//
// Shadow - 사각형 아래에 옅은 그림자를 깐다. 카드가 배경에서 떠 보이게
// 하는 용도라, 한 픽셀씩 바깥으로 갈수록 옅어지는 얇은 세 겹이면 충분하다.
func (c *Canvas) Shadow(r Rect, radius int, strength uint8) {
	for i := 1; i <= 3; i++ {
		a := strength / uint8(i*2)
		if a == 0 {
			continue
		}
		s := Rect{r.X - i, r.Y + i, r.W + 2*i, r.H}
		c.blendRoundOutline(s, radius+i, Color{}, a)
	}
}

// blendRoundOutline blends the bottom band of a rounded rectangle - its last
// row and the rows of its rounded bottom corners - by alpha. A shadow only
// needs the part that shows below the card.
// blendRoundOutline - 둥근 사각형의 아래쪽 띠 (마지막 행과 둥근 아래 모서리
// 행들) 를 알파로 섞는다. 그림자는 카드 아래로 드러나는 부분만 있으면 된다.
func (c *Canvas) blendRoundOutline(r Rect, radius int, col Color, a uint8) {
	if radius*2 > r.W {
		radius = r.W / 2
	}
	if radius*2 > r.H {
		radius = r.H / 2
	}
	blendRow := func(x, y, w int) {
		for i := 0; i < w; i++ {
			c.Set(x+i, y, Blend(c.At(x+i, y), col, a))
		}
	}
	blendRow(r.X+radius, r.Y+r.H-1, r.W-2*radius)
	for dy := 0; dy < radius; dy++ {
		inset, _ := arcInset(radius, dy)
		blendRow(r.X+inset, r.Y+r.H-1-dy, r.W-2*inset)
	}
}
