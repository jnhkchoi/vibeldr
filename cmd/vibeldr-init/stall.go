//go:build linux

package main

// stall.go reports where this program is stuck when it stops making progress.
//
// A boot that stops inside the loader stops without a word: the last line in
// the log is the one before the call that never returned, and that call could
// be any of a dozen. When nothing has been logged for stallAfter, the stacks
// of every goroutine are written to the log. The blocked one shows the exact
// call and line it is sitting in, and the kernel log (kmsg_linux.go) carries
// it out even with no serial port. It fires once per quiet spell, so a stage
// that is merely slow costs one dump, not a flood.
//
// stall.go - 이 프로그램이 진행을 멈추면 어디서 멈췄는지 보고한다.
//
// 로더 안에서 멈춘 부팅은 아무 말 없이 멈춘다. 로그의 마지막 줄은 돌아오지 않은
// 호출의 바로 앞 줄이고, 그 호출은 여러 개 중 무엇이든 될 수 있다. stallAfter
// 동안 아무것도 찍히지 않으면 모든 고루틴의 스택을 로그에 쓴다. 막힌 고루틴이
// 멈춰 있는 호출과 줄을 정확히 보여 주고, 커널 로그(kmsg_linux.go)가 시리얼
// 포트 없이도 그것을 밖으로 실어 나른다. 조용한 구간마다 한 번만 쏘므로, 그냥
// 느린 단계는 폭주가 아니라 덤프 한 번으로 끝난다.

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const stallAfter = 15 * time.Second

// lastLog is when logf last printed, in UnixNano.
// lastLog - logf 가 마지막으로 찍은 시각 (UnixNano).
var lastLog atomic.Int64

func noteProgress() { lastLog.Store(time.Now().UnixNano()) }

// watchStall runs for the life of the process.
// watchStall - 프로세스가 사는 동안 돈다.
func watchStall() {
	noteProgress()
	go func() {
		var reported int64
		for {
			time.Sleep(time.Second)
			last := lastLog.Load()
			if last == reported || time.Since(time.Unix(0, last)) < stallAfter {
				continue
			}
			reported = last
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			// Straight to the log sinks, not through logf, which would count
			// as progress.
			//
			// logf 를 거치지 않고 바로 기록한다. logf 를 거치면 진행으로 친다.
			for _, l := range strings.Split("stalled: no log line for "+stallAfter.String()+"\n"+string(buf), "\n") {
				if l != "" {
					mirrorLine("vibeldr: " + l)
				}
			}
		}
	}()
}

// watchExit records the two ways this program can vanish without a word.
//
// A write to standard output after its reader is gone raises SIGPIPE, and a Go
// program killed by that on descriptor 1 or 2 leaves nothing behind. Asking
// for the signal turns it into an ordinary write error, so the program lives
// on and says the output was cut. A panic is copied to the kernel log before
// it ends the run, since its own message goes to that same standard error.
//
// watchExit - 이 프로그램이 말없이 사라지는 두 경로를 기록한다.
//
// 표준 출력을 읽는 쪽이 사라진 뒤에 쓰면 SIGPIPE 가 나고, 서술자 1 이나 2 에서
// 그걸로 죽은 Go 프로그램은 아무것도 남기지 않는다. 시그널을 받겠다고 하면 그게
// 평범한 쓰기 오류로 바뀌어서, 프로그램이 계속 살고 출력이 끊겼다고 말한다.
// panic 은 자기 메시지가 같은
// 표준 에러로 가므로, 실행을 끝내기 전에 커널 로그에 복사한다.
func watchExit() {
	// The signals that end a Go program without a word unless asked for.
	// Anything that arrives is recorded, and all but SIGPIPE still end the
	// run as they would have.
	//
	// 받겠다고 하지 않으면 Go 프로그램을 말없이 끝내는 시그널들. 오는 것은
	// 기록하고, SIGPIPE 를 뺀 나머지는 원래대로 실행을 끝낸다.
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGALRM)
	go func() {
		s := <-term
		mirrorLine(fmt.Sprintf("vibeldr: got %v, ending this run (pid %d, parent %d)", s, os.Getpid(), os.Getppid()))
		// Ended by the same signal, as without the handler, so that systemd
		// still sees a clean stop rather than a failing exit code.
		//
		// 핸들러가 없을 때처럼 같은 시그널로 끝낸다. 그래야 systemd 가 실패한
		// 종료 코드가 아니라 정상 정지로 본다.
		signal.Reset(s)
		_ = syscall.Kill(os.Getpid(), s.(syscall.Signal))
		time.Sleep(time.Second)
		os.Exit(128 + int(s.(syscall.Signal)))
	}()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGPIPE)
	go func() {
		// Kept listening for the life of the run: stopping would put back the
		// default, and the next write would kill the program after all.
		//
		// 실행이 끝날 때까지 계속 받는다. 멈추면 기본 동작이 돌아와서 다음
		// 쓰기에서 결국 죽는다.
		<-ch
		mirrorLine("vibeldr: standard output is a broken pipe - its reader is gone; carrying on without it")
		for range ch {
		}
	}()
}

// reportPanic is deferred at the top of main.
// reportPanic - main 맨 위에서 defer 한다.
func reportPanic() {
	if r := recover(); r != nil {
		buf := make([]byte, 1<<16)
		buf = buf[:runtime.Stack(buf, false)]
		for _, l := range strings.Split(fmt.Sprintf("panic: %v\n%s", r, buf), "\n") {
			if l != "" {
				mirrorLine("vibeldr: " + l)
			}
		}
		panic(r)
	}
}
