//go:build !linux

package transcribe

func lowerPriority(int) {}

func livePriority(int) {}

func freeSpace(string) int64 { return -1 }
