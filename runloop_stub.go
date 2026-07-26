//go:build !darwin

package main

func platformRunLoop(appMain func()) {
	appMain()
}
