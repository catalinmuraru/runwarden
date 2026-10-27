//go:build !linux

package sandbox

func probe() (int, error) { return 0, ErrUnavailable }

func apply(Policy) error { return ErrUnavailable }
