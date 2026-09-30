# GRUB EFI binary (BOOTX64.EFI)

`internal/image/grub_efi.go` 가 `//go:embed grub/BOOTX64.EFI` 로 이 파일을
바이너리에 심는다. 파일이 없거나 0 바이트면 `Builder.WithEFI = true` 로
빌드할 때 `validateEFIBinary` 가 다음 메시지로 실패한다.

    grub-efi 2.14 binary not embedded at internal/image/grub/BOOTX64.EFI;
    run: go generate ./internal/image (see grub/README-EFI.md)

## 자동 (권장) — `go generate`

`grub_efi.go` 에 `//go:generate bash grub/fetch-efi.sh` 지시자가 있다.
`curl / python3 / xz / tar` 만 있으면 리눅스/macOS/git-bash 어디서든
동작한다 (`ar` 는 필요없음 — 스크립트가 Python 으로 ar 아카이브를 읽는다).

    go generate ./internal/image

스크립트는 `grub-efi-amd64-signed_1+2.14+3_amd64.deb` (Debian sid, main
아카이브의 `pool/main/g/grub-efi-amd64-signed/`) 를 받아
`usr/lib/grub/x86_64-efi-signed/grubx64.efi.signed` 를 이 디렉터리의
`BOOTX64.EFI` 로 덮어쓴다. 크기는 약 2.8 MB.

## 수동

이미 로컬에 `.deb` 이 있으면 아래로 충분하다.

    dpkg -x grub-efi-amd64-signed_1+2.14+3_amd64.deb /tmp/g
    cp /tmp/g/usr/lib/grub/x86_64-efi-signed/grubx64.efi.signed \
       internal/image/grub/BOOTX64.EFI

같은 릴리즈의 `grub-efi-amd64-bin` 은 `.mod` 만 담고 있고 monolithic
`grubx64.efi` 를 포함하지 않는다. `*-signed` 패키지가 이미 필요한 모듈이
모두 들어간 이미지를 제공하므로 그것을 그대로 사용한다. Secure Boot 서명
바이트가 붙어 있지만 서명을 검증하지 않는 펌웨어에서는 무해하고, 그대로
Removable Media 경로 (`\EFI\BOOT\BOOTX64.EFI`) 에 얹으면 부팅된다.

## 왜 리포지토리 안에 두는가

바이너리가 저장소에 커밋돼 있으므로 `git clone` 뒤 `go build` 만으로
`Builder.WithEFI = true` 빌드가 된다. 다운로드 단계를 따로 거치지 않아도
checkout 상태 그대로 EFI 경로를 쓸 수 있게 하려는 것.

License: GRUB 은 GPLv3. 재배포 의무를 위해 `usr/share/doc/grub-efi-amd64-signed/copyright`
와 changelog 도 함께 확인할 것 (원본 .deb 안에 포함).
