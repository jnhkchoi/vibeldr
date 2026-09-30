//go:build !linux

package main

// The stall watch, the exit watch and the panic record only mean anything on
// the machine the helper runs on; these keep the program building elsewhere.
//
// 멈춤 감시, 종료 감시, panic 기록은 헬퍼가 실제로 도는 머신에서만 의미가 있다.
// 이것들은 다른 곳에서도 빌드되게 한다.

func noteProgress() {}
func watchStall()   {}
func watchExit()    {}
func reportPanic()  {}
