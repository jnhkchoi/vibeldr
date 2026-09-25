// cursor.go draws the mouse cursor.
//
// The cursor must never stay on the canvas, or every move would leave a trail
// behind it. So the pixels underneath are backed up right before it is drawn
// and put back immediately after the flush to screen. The canvas stays unaware
// of the cursor, and cursor drawing never interferes with widget drawing.
//
// cursor.go - 마우스 커서 그리기.
//
// 커서는 캔버스에 영구히 남으면 안 된다. 움직일 때마다 지나간 자리에
// 자국이 남기 때문이다. 그래서 그리기 직전에 그 자리의 픽셀을 백업하고,
// 화면으로 옮긴 뒤 곧바로 되돌린다. 캔버스는 커서를 모르는 상태로
// 유지되므로 위젯 그리기와 서로 간섭하지 않는다.
package fbui

// cursorW and cursorH are the size of the cursor image.
// cursorW, cursorH - 커서 그림의 크기.
const (
	cursorW = 12
	cursorH = 19
)

// cursorMask is the cursor shape: a space is transparent, '#' is fill, '.' is
// border. A white body with a black border shows up on a light screen and on a
// dark one alike.
//
// cursorMask - 커서 모양. 공백은 투명, '#' 는 채움, '.' 은 테두리.
// 흰 화면과 짙은 화면 양쪽에서 보이도록 흰 몸통에 검은 테두리를 준다.
var cursorMask = [cursorH]string{
	".           ",
	"..          ",
	".#.         ",
	".##.        ",
	".###.       ",
	".####.      ",
	".#####.     ",
	".######.    ",
	".#######.   ",
	".########.  ",
	".#########. ",
	".#####..... ",
	".##.##.     ",
	".#. .##.    ",
	"..   .##.   ",
	"      .##.  ",
	"       .##. ",
	"        ... ",
	"            ",
}

// Cursor is the one cursor on screen.
// Cursor - 화면 위의 커서 한 개.
type Cursor struct {
	// x, y is the current position, at the cursor's tip.
	// x, y - 현재 위치 (커서 끝점).
	x, y int
	// visible says whether to draw it.
	// visible - 그릴지.
	visible bool
	// backup holds the original pixels the cursor covers, cursorW*cursorH*3 of them.
	// backup - 커서에 가려진 원래 픽셀. 크기는 cursorW*cursorH*3.
	backup []uint8
	// backedUp says whether backup is valid, and bx/by where it was taken.
	// backedUp - backup 이 유효한지와 그때의 위치.
	backedUp     bool
	bx, by       int
	fill, border Color
}

// NewCursor is a cursor with a white body and a black border.
// NewCursor - 흰 몸통, 검은 테두리 커서.
func NewCursor() *Cursor {
	return &Cursor{
		visible: true,
		backup:  make([]uint8, cursorW*cursorH*3),
		fill:    RGB(0xFFFFFF),
		border:  RGB(0x000000),
	}
}

// SetVisible shows or hides the cursor; it is hidden while an install runs.
// SetVisible - 커서를 보일지. 설치가 진행 중일 때 감추는 데 쓴다.
func (cu *Cursor) SetVisible(v bool) { cu.visible = v }

// MoveTo moves the cursor. The drawing happens in Draw.
// MoveTo - 위치를 옮긴다. 그리기는 Draw 에서 한다.
func (cu *Cursor) MoveTo(x, y int) { cu.x, cu.y = x, y }

// Bounds is the area the cursor occupies, used to work out what to flush.
// Bounds - 커서가 차지하는 영역. 화면으로 옮길 범위를 정할 때 쓴다.
func (cu *Cursor) Bounds() Rect { return Rect{cu.x, cu.y, cursorW, cursorH} }

// Draw puts the cursor on the canvas, keeping the pixels it covers. Restore has
// to be called as its pair or the canvas is left dirty.
//
// Draw - 캔버스에 커서를 얹고, 가려진 픽셀을 백업해 둔다. Restore 를
// 반드시 짝으로 불러야 캔버스가 더럽혀지지 않는다.
func (cu *Cursor) Draw(c *Canvas) {
	if !cu.visible {
		return
	}
	cu.bx, cu.by = cu.x, cu.y
	i := 0
	for row := 0; row < cursorH; row++ {
		line := cursorMask[row]
		for col := 0; col < cursorW; col++ {
			px, py := cu.bx+col, cu.by+row
			old := c.At(px, py)
			cu.backup[i] = old.R
			cu.backup[i+1] = old.G
			cu.backup[i+2] = old.B
			i += 3

			if col >= len(line) {
				continue
			}
			switch line[col] {
			case '#':
				c.Set(px, py, cu.fill)
			case '.':
				c.Set(px, py, cu.border)
			}
		}
	}
	cu.backedUp = true
}

// Restore puts back the pixels Draw overwrote.
// Restore - Draw 가 덮어쓴 픽셀을 되돌린다.
func (cu *Cursor) Restore(c *Canvas) {
	if !cu.backedUp {
		return
	}
	i := 0
	for row := 0; row < cursorH; row++ {
		for col := 0; col < cursorW; col++ {
			c.Set(cu.bx+col, cu.by+row, Color{
				R: cu.backup[i], G: cu.backup[i+1], B: cu.backup[i+2],
			})
			i += 3
		}
	}
	cu.backedUp = false
}

// LastBounds is where the cursor was last drawn. Clearing it off the screen
// means flushing that area again.
//
// LastBounds - 마지막으로 그린 자리. 그 자리를 화면에서 지우려면 이
// 영역을 다시 옮겨야 한다.
func (cu *Cursor) LastBounds() Rect {
	return Rect{cu.bx, cu.by, cursorW, cursorH}
}
