#ifndef STREAMCAP_DARWIN_H
#define STREAMCAP_DARWIN_H

// Installs window/app behaviour on the webview's NSWindow (main thread only).
void sc_setup(void *window, const void *iconPNG, int iconLen, int setIcon);
// Brings the window back and activates the app (main thread only).
void sc_show(void);

#endif
