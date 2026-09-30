//go:build !linux

// The loader's boot environment only exists on the machine it boots. This stub
// keeps the package building everywhere else, so tests and vet still cover the
// rest of the tree.
//
// 로더의 부팅 환경은 실제로 부팅하는 기계에만 있다. 이 빈 껍데기는 다른
// 곳에서도 패키지가 빌드되게 해서, 트리의 나머지를 테스트와 vet 이 계속
// 훑을 수 있게 한다.
package main

func main() {}
