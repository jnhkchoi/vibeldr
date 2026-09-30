//go:build !linux

package synoboot

import "fmt"

// clearReadOnly only exists so the package builds on a development machine.
// clearReadOnly - 개발 머신에서도 패키지가 빌드되게 하려고 둔 껍데기.
func clearReadOnly(node string) error {
	return fmt.Errorf("synoboot: cannot clear read-only on %s: not Linux", node)
}
