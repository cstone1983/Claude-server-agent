package agent

import (
	"os"
	"syscall"
)

func statOwner(st os.FileInfo) ([2]int, bool) {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return [2]int{int(s.Uid), int(s.Gid)}, true
	}
	return [2]int{}, false
}
