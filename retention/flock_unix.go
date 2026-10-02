//go:build unix

package retention

import "syscall"

// lockFile 对锁文件加排他锁，在整个操作期间持有，
// 使同一保存位置上的两个本机程序串行执行。
func lockFile(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_EX)
}

// unlockFile 释放锁。
func unlockFile(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_UN)
}
