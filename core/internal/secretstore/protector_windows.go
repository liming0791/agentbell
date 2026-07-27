//go:build windows

package secretstore

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type dpapiProtector struct{}

func (dpapiProtector) Protect(value []byte) ([]byte, error) {
	return transformDPAPI(value, true)
}

func (dpapiProtector) Unprotect(value []byte) ([]byte, error) {
	return transformDPAPI(value, false)
}

func transformDPAPI(value []byte, protect bool) ([]byte, error) {
	if len(value) == 0 || len(value) > maximumBlobBytes {
		return nil, ErrInvalidSecret
	}
	input := windows.DataBlob{
		Size: uint32(len(value)),
		Data: &value[0],
	}
	var output windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(
			&input,
			nil,
			nil,
			0,
			nil,
			windows.CRYPTPROTECT_UI_FORBIDDEN,
			&output,
		)
	} else {
		err = windows.CryptUnprotectData(
			&input,
			nil,
			nil,
			0,
			nil,
			windows.CRYPTPROTECT_UI_FORBIDDEN,
			&output,
		)
	}
	if err != nil || output.Size == 0 || output.Data == nil {
		return nil, ErrBackend
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	result := make([]byte, output.Size)
	copy(result, unsafe.Slice(output.Data, output.Size))
	return result, nil
}

func defaultProtector() protector {
	return dpapiProtector{}
}
