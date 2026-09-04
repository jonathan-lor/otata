//go:build !darwin && !linux

package app

func newSupervisor() supervisor { return none{} }
