// logview.go is the widget that shows the log as it scrolls by.
//
// It takes the lines the pipeline writes to standard output during an install
// and shows them as they are. Once there are too many, the oldest go first, and
// while the user is not scrolled up it keeps the last line in view.
//
// logview.go - 흘러가는 로그를 보여주는 위젯.
//
// 설치가 진행되는 동안 파이프라인이 표준 출력으로 쏟아내는 줄을 그대로
// 받아 보여준다. 줄 수가 많아지면 오래된 것부터 버리고, 사용자가 위로
// 올려 둔 동안이 아니면 마지막 줄이 보이게 따라간다.
package fbui

import (
	"strings"
	"sync"
)

// LogView shows a log line by line.
// LogView - 줄 단위 로그 표시.
type LogView struct {
	base
	mu sync.Mutex
	// lines are the lines held, oldest first.
	// lines - 보관 중인 줄. 앞쪽이 오래된 것.
	lines []string
	// max is how many lines to keep; beyond it the front is dropped.
	// max - 보관할 최대 줄 수. 넘으면 앞에서 버린다.
	max int
	// scroll is the index of the line shown at the top.
	// scroll - 화면 맨 위에 보일 줄의 인덱스.
	scroll int
	// follow says whether to keep jumping to the bottom as lines come in. It
	// starts on; scrolling up turns it off and scrolling back to the last line
	// turns it on again.
	//
	// follow - 새 줄이 들어오면 맨 아래로 따라갈지. 처음엔 켜져 있고, 위로
	// 굴리면 꺼지며, 마지막 줄까지 다시 내리면 켜진다.
	follow bool
	// rows is how many lines the last Draw fitted, so the wheel can tell when
	// it is back at the bottom.
	//
	// rows - 마지막으로 그릴 때 들어간 줄 수. 휠이 맨 아래로 돌아왔는지 알 때 쓴다.
	rows int
	// overwrite says the last line ended in a carriage return: like a terminal
	// cursor back at the start of that line, the next piece writes over it.
	//
	// overwrite - 마지막 줄이 캐리지 리턴으로 끝났는지. 터미널 커서가 그 줄
	// 맨 앞으로 돌아간 것처럼, 다음 조각이 그 줄을 덮어쓴다.
	overwrite bool
	// Mono draws in th.FontMono, the smallest face, instead of th.FontSmall; a
	// log has many lines and reads better small.
	// Mono - th.FontSmall 대신 가장 작은 th.FontMono 로 그릴지. 로그는 줄이
	// 많아 작은 편이 낫다.
	Mono bool
}

// NewLogView is an empty log window.
// NewLogView - 빈 로그 창.
func NewLogView() *LogView {
	return &LogView{max: 2000, follow: true, Mono: true}
}

// Append adds lines, splitting them apart when several arrive at once. It is
// safe to call from another goroutine.
//
// Append - 줄을 추가한다. 여러 줄이 한 번에 와도 쪼개서 넣는다.
// 다른 고루틴에서 불러도 된다.
func (v *LogView) Append(s string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		v.appendLine(line)
	}
	if n := len(v.lines) - v.max; n > 0 {
		v.lines = v.lines[n:]
		v.scroll -= n
		if v.scroll < 0 {
			v.scroll = 0
		}
	}
}

// appendLine adds one line the way a terminal would show it. A carriage return
// means "cursor back to the start of the line", so a progress display
// (`downloading [==  ] 3%`) writes over the same line again and again, and so
// does whatever is printed after it. A piece that ends in a carriage return
// (ScanLogLines keeps it) leaves the next piece to replace it; inside a piece
// only what follows the last carriage return is on screen.
//
// appendLine - 터미널이 보여줄 모양으로 한 줄을 넣는다. 캐리지 리턴은 "커서를
// 줄 맨 앞으로" 라서, 진행률 표시(`downloading [==  ] 3%`) 는 같은 줄을 계속
// 덮어쓰고, 그 뒤에 찍히는 글도 그 자리를 덮는다. 캐리지 리턴으로 끝난
// 조각(ScanLogLines 가 남겨 둔다) 은 다음 조각이 교체하고, 조각 안에서는
// 마지막 캐리지 리턴 뒤만 화면에 남는다.
func (v *LogView) appendLine(line string) {
	cr := strings.HasSuffix(line, "\r")
	line = strings.TrimSuffix(line, "\r")
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	// The carriage return that opens a display, with nothing before it, only
	// moves the cursor.
	// 진행률을 여는, 앞에 아무것도 없는 캐리지 리턴은 커서만 옮긴다.
	if cr && line == "" {
		return
	}
	if n := len(v.lines); v.overwrite && n > 0 {
		v.lines[n-1] = line
	} else {
		v.lines = append(v.lines, line)
	}
	v.overwrite = cr
}

// ScanLogLines is a bufio.Scanner split function for a program's output. A
// token ends at a newline or at a carriage return; one that ends at a carriage
// return keeps it, so LogView knows the next piece writes over that line.
// "\r\n" is one line break.
//
// Splitting on newlines alone would hold a progress line, which never gets a
// newline until it is finished, and the screen would look frozen.
//
// ScanLogLines - 프로그램 출력용 bufio.Scanner 분할 함수. 토큰은 개행이나
// 캐리지 리턴에서 끝나고, 캐리지 리턴에서 끝난 토큰은 그것을 달고 나온다.
// 그래야 LogView 가 다음 조각이 그 줄을 덮어쓴다는 것을 안다. "\r\n" 은 줄바꿈
// 하나다. 개행만으로 끊으면 끝날 때까지 개행이 없는 진행률 줄을 물고 있어
// 화면이 멈춘 것처럼 보인다.
func ScanLogLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		switch b {
		case '\n':
			return i + 1, data[:i], nil
		case '\r':
			if i+1 == len(data) && !atEOF {
				// Whether a newline follows is not known yet.
				// 뒤에 개행이 오는지 아직 모른다.
				return 0, nil, nil
			}
			if i+1 < len(data) && data[i+1] == '\n' {
				return i + 2, data[:i], nil
			}
			return i + 1, data[:i+1], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	// More has to be read.
	// 더 읽어야 한다.
	return 0, nil, nil
}

// Last is the final line, for summarising progress in one line elsewhere.
// Last - 마지막 줄. 진행 상황을 한 줄로 요약해 보여줄 때 쓴다.
func (v *LogView) Last() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := len(v.lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(v.lines[i]) != "" {
			return v.lines[i]
		}
	}
	return ""
}

// visibleRows is how many lines fit at this size.
// visibleRows - 이 크기에서 보이는 줄 수.
func (v *LogView) visibleRows(th *Theme) int {
	f := v.font(th)
	if f.Height() <= 0 {
		return 0
	}
	return (v.bounds.H - 8) / f.Height()
}

// font is the face to draw in, per the Mono flag.
// font - Mono 설정에 따라 쓸 글꼴.
func (v *LogView) font(th *Theme) *Face {
	if v.Mono {
		return th.FontMono
	}
	return th.FontSmall
}

// Draw paints the dark panel and the visible window of lines.
// Draw - 짙은 패널과 그 안에 보이는 구간의 줄들을 그린다.
func (v *LogView) Draw(c *Canvas, th *Theme) {
	c.FillRound(v.bounds, th.CardRadius, RGB(0x121A22))
	c.BorderRound(v.bounds, th.CardRadius, 1, RGB(0x2A3540))

	inner := v.bounds.Inset(4)
	old := c.Clip(inner)
	defer c.SetClip(old)

	f := v.font(th)
	rows := v.visibleRows(th)
	if rows <= 0 {
		return
	}

	v.mu.Lock()
	v.rows = rows
	total := len(v.lines)
	if v.follow {
		v.scroll = total - rows
	}
	if v.scroll > total-rows {
		v.scroll = total - rows
	}
	if v.scroll < 0 {
		v.scroll = 0
	}
	start := v.scroll
	end := start + rows
	if end > total {
		end = total
	}
	shown := make([]string, 0, rows)
	if start < end {
		shown = append(shown, v.lines[start:end]...)
	}
	v.mu.Unlock()

	y := inner.Y + f.Ascent()
	for _, line := range shown {
		col := RGB(0xC9D1D9)
		// Only the lines that need to stand out get a colour. The pipeline
		// marks failure and completion with these tags.
		//
		// 눈에 띄어야 하는 줄만 색을 준다. 파이프라인이 실패와 완료를
		// 이 표시로 구분해 찍는다.
		switch {
		case strings.Contains(line, "[실패]") || strings.Contains(line, "error") ||
			strings.Contains(line, "failed"):
			col = RGB(0xFF7B72)
		case strings.Contains(line, "[완료]") || strings.HasPrefix(strings.TrimSpace(line), "v "):
			col = RGB(0x7EE787)
		case strings.Contains(line, "[진행]"):
			col = RGB(0x79C0FF)
		}
		c.Text(inner.X+2, y, f.Truncate(line, inner.W-4), col, f)
		y += f.Height()
	}
}

// Handle takes the wheel and scrolls the view.
// Handle - 휠을 받아 화면을 굴린다.
func (v *LogView) Handle(e Event, s Screen) bool {
	v.trackHover(e)
	if e.Kind != EventWheel || !v.bounds.Contains(e.X, e.Y) {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.scroll -= e.Delta * 3
	bottom := len(v.lines) - v.rows
	if bottom < 0 {
		bottom = 0
	}
	if v.scroll > bottom {
		v.scroll = bottom
	}
	if v.scroll < 0 {
		v.scroll = 0
	}
	// Scrolling up stops the view following the tail; reaching the last line
	// again starts it.
	// 위로 굴리면 꼬리 따라가기가 멈추고, 마지막 줄에 다시 닿으면 재개된다.
	v.follow = v.scroll >= bottom
	return true
}

// CanFocus is false; a log view takes no keyboard input.
// CanFocus - 로그 창은 키보드 입력을 받지 않는다.
func (v *LogView) CanFocus() bool { return false }
