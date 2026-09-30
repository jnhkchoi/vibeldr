//go:build !linux

package main

// loadExtraDrivers is a no-op here. The driver pack lives on a partition this
// loader reads raw at boot, which only means anything on the machine it boots.
// The per-vendor unlocks (Aquantia, Mellanox, NVIDIA) are the same: scanning
// sysfs and calling init_module only mean anything on Linux, so a cross build
// gets a no-op.
//
// loadExtraDrivers 는 여기서 no-op 이다. 드라이버 팩은 이 로더가 부팅 때 블록
// 장치로 그대로 읽는 파티션에 있고, 그건 로더가
// 부팅시키는 머신에서만 의미가 있다. 벤더별 unlock (Aquantia / Mellanox /
// NVIDIA) 도 마찬가지다. sysfs 스캔과 init_module 은 Linux 에서만 뜻이
// 있으니 크로스빌드에서는 no-op 이다.
func loadExtraDrivers() {}
