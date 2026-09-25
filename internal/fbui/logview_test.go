package fbui

import (
	"bufio"
	"reflect"
	"strings"
	"testing"
)

// feedLog runs text through the same splitting the install screen uses and
// appends every piece.
//
// feedLog - 설치 화면과 같은 방식으로 글을 쪼개 하나씩 넣는다.
func feedLog(v *LogView, text string) {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Split(ScanLogLines)
	for sc.Scan() {
		v.Append(sc.Text())
	}
}

// TestLogViewFollowResumesAtBottom: scrolling up stops the view following new
// lines, and scrolling back down to the last line starts it again. Without the
// second half one turn of the wheel would freeze the view for the rest of the
// install.
//
// TestLogViewFollowResumesAtBottom - 위로 굴리면 따라가기가 멈추고, 맨 아래로
// 되돌리면 다시 따라간다. 뒤쪽이 없으면 휠 한 번에 설치 끝까지 화면이 멈춘다.
func TestLogViewFollowResumesAtBottom(t *testing.T) {
	c := NewCanvas(400, 200)
	th := DefaultTheme()
	v := NewLogView()
	v.SetBounds(Rect{0, 0, 400, 200})
	for i := 0; i < 100; i++ {
		v.Append("line")
	}
	v.Draw(c, th)

	up := Event{Kind: EventWheel, X: 10, Y: 10, Delta: 1}
	down := Event{Kind: EventWheel, X: 10, Y: 10, Delta: -1}
	v.Handle(up, nil)
	if v.follow {
		t.Fatal("위로 굴렸는데 계속 따라감")
	}
	v.Handle(down, nil)
	if !v.follow {
		t.Fatal("맨 아래로 돌아왔는데 따라가기가 다시 켜지지 않음")
	}
	v.Append("new")
	v.Draw(c, th)
	if want := len(v.lines) - v.visibleRows(th); v.scroll != want {
		t.Fatalf("scroll = %d, want %d (the last line in view)", v.scroll, want)
	}
}

// TestLogViewProgressStaysOnOneLine: a progress display rewrites one line with
// carriage returns, and the log shows what a terminal would - one line, which
// whatever comes after the last carriage return writes over. ui.ClearLine is a
// carriage return plus an escape the install screen strips, so the line after
// a display lands in its place, as on a terminal.
//
// TestLogViewProgressStaysOnOneLine - 진행률은 캐리지 리턴으로 한 줄을 고쳐
// 쓰고, 로그는 터미널이 보여줄 것을 보여준다. 한 줄이고, 마지막 캐리지 리턴
// 뒤에 오는 글이 그 위를 덮는다. ui.ClearLine 은 캐리지 리턴과 설치 화면이
// 걷어내는 escape 라, 진행률 다음 줄이 터미널에서처럼 그 자리에 온다.
func TestLogViewProgressStaysOnOneLine(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want []string
	}{
		"ends with a newline": {
			"== Fetch\n\r  downloading [#---]  25.0%\r  downloading [##--]  50.0%\r  downloading [####] 100.0%\n  v wrote x\n",
			[]string{"== Fetch", "  downloading [####] 100.0%", "  v wrote x"},
		},
		"ends with a clear-line": {
			"== Write payload\n\r  flashing p3 [#-]  50.0%\r  flashing p3 [##] 100.0%\r  v wrote 400 MB\n",
			[]string{"== Write payload", "  v wrote 400 MB"},
		},
		"two displays in a row": {
			"\r  flashing p3 [##] 100.0%\r  v wrote p3\n\r  flashing p4 [#-]  50.0%\r  flashing p4 [##] 100.0%\r  v wrote p4\n",
			[]string{"  v wrote p3", "  v wrote p4"},
		},
		"carriage return and newline is one line break": {
			"a\r\nb\r\n",
			[]string{"a", "b"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := NewLogView()
			feedLog(v, tc.text)
			if !reflect.DeepEqual(v.lines, tc.want) {
				t.Errorf("lines = %q\nwant    %q", v.lines, tc.want)
			}
		})
	}
}

// TestLogViewKeepsOrdinaryBrackets: a line that is not being written over is
// never replaced, even when it has a bracket or a percent sign in it.
//
// TestLogViewKeepsOrdinaryBrackets - 덮어쓰는 줄이 아니면 대괄호나 퍼센트가
// 있어도 교체하지 않는다.
func TestLogViewKeepsOrdinaryBrackets(t *testing.T) {
	v := NewLogView()
	feedLog(v, "[진행] 1\n[진행] 2\n50% done\n")
	if want := []string{"[진행] 1", "[진행] 2", "50% done"}; !reflect.DeepEqual(v.lines, want) {
		t.Errorf("lines = %q, want %q", v.lines, want)
	}
}
