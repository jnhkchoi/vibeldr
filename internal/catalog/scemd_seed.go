package catalog

// scemd_seed.go - the "seed DSM" images that Synology's own extractor (scemd)
// is taken out of.
//
// A .pat is a tar holding zImage and rd.gz, but from DSM 7.1 onwards it is
// locked inside Synology's own container and will not simply open. The only
// thing that opens it is Synology's own binary, scemd (also known as
// syno_extract_system_patch), and that binary lives inside DSM. A current DSM
// is itself locked, so a release from before the lock is downloaded and the
// binary taken out of that.
//
// What makes a usable seed: the .pat has to be a plain tar, which is the case
// when "ustar" appears at offset 257 of the first 512 bytes. DSM up to 7.0.1
// is plain and 7.1.1 onwards is locked. It also has to be an x86-64 model, or
// the extracted binary will not run.
//
// 7.0.1 comes before 6.2.4 because the 6.2.4 scemd does not know the newer
// container. The 7.0.1 one ships with the mbedtls / libsodium /
// libsynocodesign family alongside it and opens the .pat files published today.
//
// scemd_seed.go - 시놀로지 자체 추출기 (scemd) 를 꺼내 올 "씨앗 DSM" 목록.
//
// .pat 은 zImage 와 rd.gz 를 담은 tar 인데, DSM 7.1 부터 시놀로지 자체
// 컨테이너로 잠겨 있어 그냥은 열리지 않는다. 여는 건 시놀로지 자체 바이너리
// scemd (aka syno_extract_system_patch) 뿐이고, 그 바이너리는 DSM 안에 들어
// 있다. 최신 DSM 은 잠겨 있어 꺼낼 수 없으므로, 잠금이 없던 시절의 DSM 을
// 받아 거기서 꺼낸다.
//
// 후보의 조건: .pat 이 평문 tar 여야 한다. 첫 512 바이트의 offset 257 에
// "ustar" 가 있으면 평문이다. DSM 7.0.1 까지가 평문이고 7.1.1 부터 잠겨
// 있다. 그리고 x86-64 모델이어야 꺼낸 바이너리를 그대로 실행할 수 있다.
//
// 7.0.1 을 6.2.4 보다 앞에 두는 이유: 6.2.4 의 scemd 는 최신 컨테이너를
// 모른다. 7.0.1 쪽은 mbedtls / libsodium / libsynocodesign 계열을 함께
// 담고 있어 지금 배포되는 .pat 을 연다.

// ScemdSeed is one seed DSM.
// ScemdSeed - 씨앗 DSM 한 개.
type ScemdSeed struct {
	// Label is the short name used in logs and errors ("DS918+ 7.0.1-42218").
	// Label 은 로그/에러에 쓰이는 짧은 이름 ("DS918+ 7.0.1-42218" 등).
	Label string
	// URL is the exact location of the .pat on Synology's CDN.
	// URL 은 시놀로지 CDN 상의 정확한 .pat 위치.
	URL string
	// MD5 is 32 lower-case hex digits. An empty string skips verification.
	// MD5 는 소문자 hex 32자. 빈 문자열이면 검증 skip.
	MD5 string
}

// ScemdSeeds are tried in order and the first success wins, so the most
// dependable one goes first.
//
// ScemdSeeds - 순차 시도 후보. 첫 성공에서 멈추므로 신뢰도 높은 것을 앞에.
var ScemdSeeds = []ScemdSeed{
	{
		Label: "DS918+ 7.0.1-42218",
		URL:   "https://global.synologydownload.com/download/DSM/release/7.0.1/42218/DSM_DS918%2B_42218.pat",
	},
	{
		Label: "DS3622xs+ 7.0.1-42218",
		URL:   "https://global.synologydownload.com/download/DSM/release/7.0.1/42218/DSM_DS3622xs%2B_42218.pat",
	},
}

// Where the extracted files are placed inside the boot image.
//
// DSM's own layout is kept as it was. The binary looks its crypto libraries up
// by name at run time, so renaming or moving them means it will not find them.
//
// The file name is the one exception. Inside the seed ramdisk it is scemd, but
// this is a multi-call binary that decides what to do from its own argv[0]:
// called as scemd it extracts nothing and exits quietly. The extraction
// behaviour appears when it is called syno_extract_system_patch.
//
// 추출한 것들을 부트 이미지 안에 놓는 자리.
//
// DSM 이 쓰던 배치를 그대로 유지한다. 이 바이너리가 크립토 라이브러리를
// 실행 중에 이름으로 찾아 열기 때문에, 라이브러리 이름과 배치를 바꾸면
// 못 찾는다.
//
// 파일 이름만은 바꿔서 놓는다. 씨앗 램디스크 안에서는 `scemd` 지만, 이
// 바이너리는 자기 argv[0] 을 보고 할 일을 정하는 멀티콜 바이너리라서
// `scemd` 로 부르면 추출을 하지 않고 조용히 끝난다. 추출 동작은
// `syno_extract_system_patch` 라는 이름으로 불렸을 때 나온다.
const (
	ScemdBundleDir = "usr/lib"
	// ScemdMemberPath is where it sits inside the seed ramdisk.
	// ScemdMemberPath - 씨앗 램디스크 안에서의 위치.
	ScemdMemberPath = "usr/syno/bin/scemd"
	// ScemdBinPath is where it sits in our image, and the name it is run under.
	// ScemdBinPath - 우리 이미지 안에서의 위치이자 실행할 때의 이름.
	ScemdBinPath = "usr/syno/bin/syno_extract_system_patch"
)
