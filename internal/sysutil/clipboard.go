package sysutil

// maxClipboard caps how much clipboard text is read: share messages are short, and a huge copied
// document shouldn't be scanned every time the window gets focus.
const maxClipboard = 64 << 10
