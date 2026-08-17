//go:build unix

package sandboxsvc

import (
	"os"
	"syscall"
)

// fileOwnerID 返回文件/目录属主 uid；获取失败返回 -1。
// 供 fixUserDir 判断用户目录属主是否已是派生 uid（决定是否触发旧卷迁移）。
func fileOwnerID(st os.FileInfo) int {
	if st == nil {
		return -1
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid)
	}
	return -1
}

// fileGroupID 返回文件/目录属组 gid；获取失败返回 -1。
// 供 fixUserDir 判断用户目录属组是否已是 app 组（决定是否触发旧卷迁移）。
func fileGroupID(st os.FileInfo) int {
	if st == nil {
		return -1
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(stat.Gid)
	}
	return -1
}
