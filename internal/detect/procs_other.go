//go:build !linux && !windows

package detect

import "errors"

type sysLister struct{}

// NewLister: process scanning is not implemented on this OS (v0.1 targets Windows).
func NewLister() Lister { return sysLister{} }

func (sysLister) List() ([]Proc, error)      { return nil, errors.New("unsupported OS") }
func (sysLister) CPU(int32) (float64, error) { return 0, errors.New("unsupported OS") }
func (sysLister) Cmdline(int32) string       { return "" }
