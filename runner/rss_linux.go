package main

import "syscall"

// ru_maxrss is kilobytes on Linux.
func rssKB(ru *syscall.Rusage) int64 { return ru.Maxrss }
