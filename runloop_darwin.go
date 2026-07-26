//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

void StartNSRunLoop(void) {
	@autoreleasepool {
		[NSApplication sharedApplication];
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		[NSApp run];
	}
}

void StopNSRunLoop(void) {
	[NSApp performSelectorOnMainThread:@selector(terminate:)
	                        withObject:nil
	                     waitUntilDone:NO];
}
*/
import "C"

import "runtime"

func init() {
	runtime.LockOSThread()
}

func platformRunLoop(appMain func()) {
	go func() {
		appMain()
		C.StopNSRunLoop()
	}()
	C.StartNSRunLoop()
}
