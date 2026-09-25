// Package ui - the small amount of terminal formatting the CLI needs.
//
// No dependencies and no TUI framework. The workflow is declarative, so the
// output is something to read rather than something to interact with. A
// richer front end can sit on top of this without changing it, as the
// graphical installer does through OnSection and OnProgress.
//
// Package ui - CLI 가 필요로 하는 최소한의 터미널 포맷팅.
//
// 의존성 없음, TUI 프레임워크 없음. 선언적 워크플로우라 출력은 읽는 것이지
// 상호작용하는 것이 아님. 더 화려한 프런트엔드가 이 위에 얹혀도 이쪽은
// 그대로 둬도 된다. 그래픽 설치 화면이 OnSection, OnProgress 로 그렇게 쓴다.
package ui

import (
	"fmt"
	"os"
	"strings"
)

var useColor = supportsColor()

func supportsColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return true
}

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	cyan   = "\033[36m"
)

func paint(code, s string) string {
	if !useColor {
		return s
	}
	return code + s + reset
}

func Bold(s string) string   { return paint(bold, s) }
func Dim(s string) string    { return paint(dim, s) }
func Red(s string) string    { return paint(red, s) }
func Green(s string) string  { return paint(green, s) }
func Yellow(s string) string { return paint(yellow, s) }
func Blue(s string) string   { return paint(blue, s) }
func Cyan(s string) string   { return paint(cyan, s) }

// OnSection and OnProgress, when set, are told about every Section and
// ProgressBar as well as the text going out. The graphical installer uses them
// for its overall progress bar instead of reading the text back.
//
// OnSection, OnProgress - 설정해 두면 Section 과 ProgressBar 가 글을 찍을 때마다
// 함께 알려준다. 그래픽 설치 화면이 글을 다시 읽는 대신 전체 진행 막대에 쓴다.
var (
	OnSection  func(title string)
	OnProgress func(label string, done, total int64)
)

// Section prints a sub-heading.
// Section - 소제목을 출력.
func Section(title string) {
	if OnSection != nil {
		OnSection(title)
	}
	fmt.Println()
	fmt.Println(Bold(Blue("== " + title)))
}

// Field prints a label and its value, aligned into a column.
// Field - 라벨/값 쌍을 정렬해서 출력.
func Field(label, value string) {
	fmt.Printf("  %-22s %s\n", label+":", value)
}

// Info, OK, Warn and Fail are the four message levels the CLI uses.
// Info, OK, Warn, Fail - CLI 가 쓰는 네 가지 메시지 레벨.
func Info(format string, a ...any) { fmt.Printf("  %s\n", fmt.Sprintf(format, a...)) }
func OK(format string, a ...any)   { fmt.Printf("  %s %s\n", Green("v"), fmt.Sprintf(format, a...)) }
func Warn(format string, a ...any) { fmt.Printf("  %s %s\n", Yellow("!"), fmt.Sprintf(format, a...)) }
func Fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "  %s %s\n", Red("x"), fmt.Sprintf(format, a...))
}

// Bytes renders a byte count in the largest unit that keeps it readable.
// Bytes - 읽기 좋은 최대 단위로 바이트 카운트 렌더.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ProgressBar redraws a progress line in place. A total of zero or less means
// the size is not known, and a plain byte counter is shown instead.
//
// ProgressBar - in-place 진행 상황 줄 렌더. total 이 0 이하면 크기 모름으로
// 간주하고 바이트 카운터로 대체.
func ProgressBar(label string, done, total int64) {
	if OnProgress != nil {
		OnProgress(label, done, total)
	}
	if total <= 0 {
		fmt.Printf("\r  %s %s", label, Bytes(done))
		return
	}
	const width = 32
	ratio := float64(done) / float64(total)
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio * width)
	bar := strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
	fmt.Printf("\r  %s [%s] %5.1f%%  %s / %s",
		label, bar, ratio*100, Bytes(done), Bytes(total))
}

// ClearLine erases the in-place progress line and leaves the cursor at its
// start, so the next output overwrites it.
// ClearLine - in-place 진행 상황 줄을 지우고 커서를 줄 맨 앞에 둔다. 다음
// 출력이 그 자리에 찍힌다.
func ClearLine() { fmt.Print("\r\033[K") }
