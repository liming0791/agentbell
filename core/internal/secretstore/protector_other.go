//go:build !windows

package secretstore

type unsupportedProtector struct{}

func (unsupportedProtector) Protect([]byte) ([]byte, error) {
	return nil, ErrUnavailable
}

func (unsupportedProtector) Unprotect([]byte) ([]byte, error) {
	return nil, ErrUnavailable
}

func defaultProtector() protector {
	return unsupportedProtector{}
}
