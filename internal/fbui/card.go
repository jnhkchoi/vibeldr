// card.go is the card that groups values together.
//
// Laying labels and values out plainly makes the screen read as a table and
// hides which things belong together. One layer of background behind a group
// lets the reader see the unit at a glance.
//
// card.go - 값들을 묶어 보여주는 카드.
//
// 라벨과 값을 그냥 늘어놓으면 화면이 표처럼 보이고 무엇이 한 덩어리인지
// 알기 어렵다. 배경을 한 겹 깔아 묶으면 읽는 사람이 단위를 바로 알아본다.
package fbui

// Card is the box holding a title and "label : value" rows.
// Card - 제목과 "라벨 : 값" 줄들을 담는 상자.
type Card struct {
	base
	// Title is the heading at the top; empty draws no title row.
	// Title - 카드 위쪽 제목. 비우면 제목 줄을 그리지 않는다.
	Title string
	// Rows are the items to show, drawn in order.
	// Rows - 보여줄 항목. 순서대로 그린다.
	Rows []CardRow
	// LabelW is the width of the label column.
	// LabelW - 라벨 열의 폭.
	LabelW int
}

// CardRow is one row inside a card.
// CardRow - 카드 안의 한 줄.
type CardRow struct {
	Label string
	Value string
	// Accent draws the value in the accent colour, to make one important
	// value stand out.
	//
	// Accent - 값을 강조색으로 그릴지. 중요한 값 하나를 눈에 띄게 할 때.
	Accent bool
	// Dim draws the value faded, for a value that is not settled yet.
	// Dim - 값을 흐리게 그릴지. 아직 정해지지 않은 값에 쓴다.
	Dim bool
}

// NewCard builds a card from a title and its rows.
// NewCard - 제목과 줄들로 카드를 만든다.
func NewCard(title string, rows ...CardRow) *Card {
	return &Card{Title: title, Rows: rows, LabelW: 170}
}

// CardHeight is the card height for a given number of rows. It is exported
// because the layout has to work it out in advance.
//
// CardHeight - 줄 수에 맞는 카드 높이. 배치할 때 미리 계산해야 해서
// 밖으로 낸다.
func CardHeight(th *Theme, title string, rows int) int {
	h := th.Pad
	if title != "" {
		h += th.FontBody.Height() + 6
	}
	h += rows * (th.RowH - 4)
	return h + th.Pad
}

// CanFocus is false: a card shows values and takes no input.
// CanFocus - 카드는 값을 보여줄 뿐 입력을 받지 않는다.
func (c *Card) CanFocus() bool { return false }

// Draw paints the card background, its title and every row.
// Draw - 카드 배경과 제목, 각 줄을 그린다.
func (c *Card) Draw(cv *Canvas, th *Theme) {
	cv.FillRound(c.bounds, th.CardRadius, RGB(0xFFFFFF))
	cv.BorderRound(c.bounds, th.CardRadius, 1, Blend(th.Panel, th.Border, 150))

	x := c.bounds.X + th.Pad
	w := c.bounds.W - th.Pad*2
	y := c.bounds.Y + th.Pad

	if c.Title != "" {
		cv.TextIn(Rect{x, y, w, th.FontBody.Height()}, c.Title, th.Text, th.FontBody, -1)
		y += th.FontBody.Height() + 6
	}

	rowH := th.RowH - 4
	for _, row := range c.Rows {
		cv.TextIn(Rect{x, y, c.LabelW, rowH}, row.Label, th.TextDim, th.FontSmall, -1)
		col := th.Text
		switch {
		case row.Accent:
			col = th.Accent
		case row.Dim:
			col = th.TextDim
		}
		vr := Rect{x + c.LabelW, y, w - c.LabelW, rowH}
		cv.TextIn(vr, th.FontBody.Truncate(row.Value, vr.W), col, th.FontBody, -1)
		y += rowH
	}
}

// Handle ignores every event.
// Handle - 이벤트를 받지 않는다.
func (c *Card) Handle(Event, Screen) bool { return false }
