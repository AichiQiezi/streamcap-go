#ifndef STREAMCAP_DARWIN_H
#define STREAMCAP_DARWIN_H

// Installs window/app behaviour on the webview's NSWindow (main thread only).
void sc_setup(void *window, const void *iconPNG, int iconLen, int setIcon);
// Brings the window back and activates the app (main thread only).
void sc_show(void);
// Posts a Notification Center notification; clicking it opens url (may be empty). Blocks until the
// system accepted or refused it, so call it off the main thread. Returns NULL on success, otherwise
// a malloc'd message the caller frees. Requires running from an .app bundle.
char *sc_notify(const char *title, const char *body, const char *url);

// Opens url in an off-screen WKWebView and forwards every frame of the page's comment websocket to
// goDanmakuFrame(id, base64). Runs on the main thread's run loop, so the app must have one.
void sc_watcher_open(const char *url, long long id);
// Closes and releases the watcher (safe from any thread; ignored when it never opened).
void sc_watcher_close(long long id);

#endif
