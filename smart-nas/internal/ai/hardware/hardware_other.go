//go:build !windows && !linux

package hardware

func detectOS() string { return "unknown" }

func detectTotalMemory() float64 { return 0 }

func detectCPUModel() string     { return "" }
