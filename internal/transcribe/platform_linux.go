package transcribe

import "syscall"

// lowerPriority runs a process at nice 10. Its threads, started after
// this, inherit it.
func lowerPriority(pid int) { _ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10) }

// livePriority runs the live recogniser for live transcription at nice 4: ahead
// of transcripts after calls (nice 10), behind the calls' audio.
func livePriority(pid int) { _ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 4) }

// freeSpace is the space left on the file system holding dir (bytes; -1
// when unknown).
func freeSpace(dir string) int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
