package web

import "syscall"

// diskUsage returns the size and free space, in bytes, of the disk that
// holds path.
func diskUsage(path string) (total, free uint64, ok bool) {
	var st syscall.Statfs_t
	if path == "" || syscall.Statfs(path, &st) != nil {
		if syscall.Statfs("/", &st) != nil {
			return 0, 0, false
		}
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize), true
}
