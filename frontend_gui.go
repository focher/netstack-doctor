//go:build !headless

package main

import (
	"runtime"

	webview "github.com/webview/webview_go"
)

// gw holds the native window so handleQuit can close it from any goroutine.
var gw webview.WebView

func init() {
	// webview must own the main OS thread (required by Cocoa/WKWebView on macOS).
	runtime.LockOSThread()
}

// runFrontend opens the standalone native application window and blocks until
// the user closes it. No external browser is involved.
func runFrontend(url string) {
	gw = webview.New(false)
	defer gw.Destroy()
	gw.SetTitle("NetStack Doctor")
	gw.SetSize(1180, 840, webview.HintNone)
	gw.SetSize(900, 640, webview.HintMin)
	gw.Navigate(url)
	gw.Run()
}

// requestQuit terminates the native window (and thus the app) from the UI.
func requestQuit() {
	if gw != nil {
		gw.Terminate()
	}
}
