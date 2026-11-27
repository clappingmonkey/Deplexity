//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package main

import "errors"

func hideInput(int) (func() error, error) {
	return nil, errors.New("hiding terminal input is not supported on this platform")
}
