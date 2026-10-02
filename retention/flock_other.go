//go:build !unix

package retention

// 非 unix 平台没有 flock；进程内仍由互斥锁串行，
// 跨进程串行依赖保存位置所在文件系统的原子重命名保证。
func lockFile(fd uintptr) error   { return nil }
func unlockFile(fd uintptr) error { return nil }
