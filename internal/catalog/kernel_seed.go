// kernel_seed.go - where the generic Linux kernel for the bootstrap image
// comes from.
//
// A bootstrap image is the one built before the model and DSM version have
// been chosen. Putting a Synology zImage in it would decide the model, so it
// needs a generic kernel that boots on any x86_64 machine instead. Alpine
// Linux's linux-lts package fits exactly: a small LTS kernel with the drivers
// needed for early boot compiled in, so an initramfs alone is enough for
// vibeldr-boot to bring up its TUI on a serial or VGA console.
//
// kernel_seed.go - 부트스트랩 이미지에 실을 제네릭 리눅스 커널의 소스.
//
// 부트스트랩 이미지 = "모델/DSM 결정 전" 이미지. 이 안에 시놀로지 zImage 를
// 싣는 순간 모델이 결정돼 버리므로, 대신 아무 x86_64 머신에서 그대로 부팅
// 되는 제네릭 커널이 필요하다. Alpine Linux 의 `linux-lts` 패키지가 딱 이
// 요구에 맞는다 - 소형 LTS 커널이고 초기 부팅에 필요한 드라이버가 내장이라
// initramfs 만으로 vibeldr-boot 이 시리얼/VGA 콘솔에서 TUI 를 띄울 수 있다.

package catalog

// AlpineMirror is one mirror of the Alpine Linux repository, down to the
// architecture directory.
//
// APKINDEX.tar.gz is fetched from under this base, and the file name taken
// from it is appended to the same base to download the .apk.
//
// AlpineMirror - Alpine Linux 저장소의 한 미러 (arch 디렉터리까지).
//
// APKINDEX.tar.gz 를 이 base 아래에서 받고, 거기서 뽑은 파일명을 그대로
// base 뒤에 붙여 .apk 를 내려받는다.
type AlpineMirror struct {
	// Label is a short name so a human can tell which mirror the log means.
	// Label 은 로그에서 사람이 알아볼 수 있게 하는 짧은 이름.
	Label string
	// Base is the architecture directory URL, with no trailing slash, e.g.
	// "https://dl-cdn.alpinelinux.org/alpine/v3.20/main/x86_64".
	//
	// Base 는 arch 디렉터리 URL. 끝에 슬래시 없음.
	Base string
}

// AlpineMirrors are tried in order, stopping at the first that works.
//
// Why this order:
//
//  1. v3.20 has the widest support window right now. Its 6.6 LTS kernel
//     recognises most x86_64 hardware as shipped.
//  2. v3.19 covers a moment when the v3.20 mirror is down. Same 6.6 LTS line.
//  3. latest-stable is the last resort: it stays valid even when the release
//     names change.
//
// AlpineMirrors - 순차 시도 후보. 첫 성공에서 멈춘다.
//
// 순서 이유:
//
//  1. v3.20 은 현재 지원 창구가 가장 넓은 릴리스. 커널 6.6 LTS 계열이라
//     대부분의 x86_64 하드웨어를 그대로 인식한다.
//  2. v3.19 는 v3.20 미러가 잠깐 죽었을 때 대안. 같은 6.6 LTS 계열.
//  3. latest-stable 은 릴리스 이름이 바뀌어도 항상 유효한 마지막 안전망.
var AlpineMirrors = []AlpineMirror{
	{Label: "alpine v3.20", Base: "https://dl-cdn.alpinelinux.org/alpine/v3.20/main/x86_64"},
	{Label: "alpine v3.19", Base: "https://dl-cdn.alpinelinux.org/alpine/v3.19/main/x86_64"},
	{Label: "alpine latest-stable", Base: "https://dl-cdn.alpinelinux.org/alpine/latest-stable/main/x86_64"},
}

// AlpineKernelPackage is the package name (P:) to look up in APKINDEX.
//
// linux-lts is Alpine's long-term-support kernel meta package. Inside the
// archive the kernel is boot/vmlinuz-lts and the modules are under
// lib/modules/<ver>-lts.
//
// AlpineKernelPackage - APKINDEX 에서 뽑을 패키지 이름 (P:).
//
// linux-lts 는 Alpine 의 장기지원 커널 메타 패키지. vmlinuz 는
// boot/vmlinuz-lts 로, 모듈은 lib/modules/<ver>-lts 로 아카이브 안에
// 들어 있다.
const AlpineKernelPackage = "linux-lts"

// AlpineKernelVMLinuzPath is where vmlinuz sits inside the .apk archive.
// AlpineKernelVMLinuzPath - .apk 아카이브 안의 vmlinuz 상대 경로.
const AlpineKernelVMLinuzPath = "boot/vmlinuz-lts"

// AlpineCACertPackage is the CA certificate bundle package name (P:).
//
// The boot environment fetches the .pat from Synology's CDN over HTTPS. With
// no trust store, certificate verification fails and nothing downloads at all.
//
// AlpineCACertPackage - CA 인증서 번들 패키지 이름 (P:).
//
// 부트 환경은 시놀로지 CDN 에서 .pat 을 HTTPS 로 받는다. 신뢰 저장소가
// 없으면 인증서 검증이 실패해 다운로드 자체가 안 된다.
const AlpineCACertPackage = "ca-certificates-bundle"

// AlpineCACertPath is where the bundle lives inside the .apk, and where it is
// placed inside the initrd.
//
// It is the location Go's x509 looks in by default on Linux, so putting it
// there means no code has to name it.
//
// AlpineCACertPath - .apk 안의 번들 경로이자 initrd 안에 놓을 경로.
//
// Go 의 x509 가 리눅스에서 기본으로 찾는 위치라, 이 경로에 두면 코드에서
// 따로 지정하지 않아도 잡힌다.
const AlpineCACertPath = "etc/ssl/certs/ca-certificates.crt"

// XZFiles are the xz binary and everything it needs, carried in the boot image.
//
// Turning off module signature enforcement in the DSM kernel means flipping one
// byte inside the kernel image, and that byte sits inside an LZMA-compressed
// blob. Changing it means decompressing the whole thing, editing it, and
// compressing it back into the same slot - a slot of fixed size, so a result
// larger than the original will not fit. Synology's compressor was liblzma, so
// carrying the same liblzma is what produces the same size.
//
// Each entry names an apk and the path to take out of it. The extracted path is
// also where the file is placed in the image.
//
// XZFiles - 부트 이미지에 실을 xz 실행 파일과 그것이 필요로 하는 것들.
//
// DSM 커널에서 모듈 서명 강제를 끄려면 커널 이미지 안의 한 바이트를
// 뒤집어야 하는데, 그 바이트가 LZMA 로 압축된 덩어리 안에 있다. 한 바이트를
// 바꾸려면 통째로 풀었다가 다시 압축해서 원래 칸에 도로 넣어야 하고, 칸
// 크기가 고정이라 압축 결과가 원본보다 크면 넣을 수 없다. 시놀로지가 쓴
// 압축기가 곧 liblzma 이므로, 같은 liblzma 를 실어서 같은 크기를 낸다.
//
// 각 항목은 apk 이름과 그 안에서 꺼낼 경로. 꺼낸 경로 그대로 이미지에 놓는다.
var XZFiles = []struct {
	Package string
	Path    string
}{
	{"xz", "usr/bin/xz"},
	{"xz-libs", "usr/lib/liblzma.so.5"},
	{"musl", "lib/ld-musl-x86_64.so.1"},
	// The name xz asks for via DT_NEEDED. On musl this is the same file as the
	// dynamic linker.
	//
	// xz 가 DT_NEEDED 로 요구하는 이름. musl 에서는 동적 링커와 같은 파일이다.
	{"musl", "lib/libc.musl-x86_64.so.1"},
}

// XZBinPath is where xz sits inside the image.
// XZBinPath - 이미지 안에서의 xz 위치.
const XZBinPath = "usr/bin/xz"
