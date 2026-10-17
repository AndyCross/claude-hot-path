package main

import "syscall"

// ru_maxrss is bytes on Darwin.
func rssKB(ru *syscall.Rusage) int64 { return ru.Maxrss / 1024 }
