//go:build unix && !darwin && !linux

package auth

func isZombie(int) bool { return false }
