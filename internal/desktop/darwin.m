//go:build darwin

#import <Cocoa/Cocoa.h>
#import <UserNotifications/UserNotifications.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>
#include "darwin.h"

extern void goQuitRequested(void);
extern void goDanmakuFrame(long long wid, const void *data, int len);

static NSWindow *scWindow = nil;

// Close button / Cmd+W: hide instead of closing, so recording keeps running.
static BOOL sc_windowShouldClose(id self, SEL _cmd, id sender) {
    [(NSWindow *)sender orderOut:nil];
    return NO;
}

// Clicking the Dock icon while the window is hidden brings it back.
static BOOL sc_shouldHandleReopen(id self, SEL _cmd, id app, BOOL hasVisibleWindows) {
    sc_show();
    return YES;
}

// Cmd+Q / Dock → Quit: run the app's graceful shutdown instead of exiting immediately.
// The app calls Terminate (which stops the run loop) once recordings are saved.
static NSApplicationTerminateReply sc_shouldTerminate(id self, SEL _cmd, id app) {
    goQuitRequested();
    return NSTerminateCancel;
}

static void addMethod(Class cls, SEL sel, IMP imp, const char *types) {
    if (!class_addMethod(cls, sel, imp, types)) {
        class_replaceMethod(cls, sel, imp, types);
    }
}

static NSMenuItem *item(NSString *title, SEL action, NSString *key, NSEventModifierFlags mods) {
    NSMenuItem *it = [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key];
    if (mods) [it setKeyEquivalentModifierMask:mods];
    return it;
}

// webview sets no main menu, so without this Cmd+C / Cmd+V / Cmd+Q do nothing.
static void buildMenu(void) {
    NSMenu *bar = [[NSMenu alloc] init];

    NSMenuItem *appItem = [[NSMenuItem alloc] init];
    NSMenu *appMenu = [[NSMenu alloc] initWithTitle:@"StreamCap"];
    [appMenu addItem:item(@"关于 StreamCap", @selector(orderFrontStandardAboutPanel:), @"", 0)];
    [appMenu addItem:[NSMenuItem separatorItem]];
    [appMenu addItem:item(@"隐藏 StreamCap", @selector(hide:), @"h", 0)];
    [appMenu addItem:item(@"隐藏其他", @selector(hideOtherApplications:), @"h", NSEventModifierFlagCommand | NSEventModifierFlagOption)];
    [appMenu addItem:[NSMenuItem separatorItem]];
    [appMenu addItem:item(@"退出 StreamCap", @selector(terminate:), @"q", 0)];
    [appItem setSubmenu:appMenu];
    [bar addItem:appItem];

    NSMenuItem *editItem = [[NSMenuItem alloc] init];
    NSMenu *edit = [[NSMenu alloc] initWithTitle:@"编辑"];
    [edit addItem:item(@"撤销", @selector(undo:), @"z", 0)];
    [edit addItem:item(@"重做", @selector(redo:), @"z", NSEventModifierFlagCommand | NSEventModifierFlagShift)];
    [edit addItem:[NSMenuItem separatorItem]];
    [edit addItem:item(@"剪切", @selector(cut:), @"x", 0)];
    [edit addItem:item(@"拷贝", @selector(copy:), @"c", 0)];
    [edit addItem:item(@"粘贴", @selector(paste:), @"v", 0)];
    [edit addItem:item(@"全选", @selector(selectAll:), @"a", 0)];
    [editItem setSubmenu:edit];
    [bar addItem:editItem];

    NSMenuItem *winItem = [[NSMenuItem alloc] init];
    NSMenu *win = [[NSMenu alloc] initWithTitle:@"窗口"];
    [win addItem:item(@"最小化", @selector(performMiniaturize:), @"m", 0)];
    [win addItem:item(@"关闭窗口", @selector(performClose:), @"w", 0)];
    [winItem setSubmenu:win];
    [bar addItem:winItem];
    [NSApp setWindowsMenu:win];

    [NSApp setMainMenu:bar];
}

void sc_setup(void *window, const void *iconPNG, int iconLen, int setIcon) {
    scWindow = (__bridge NSWindow *)window;

    id winDelegate = [scWindow delegate];
    if (winDelegate) {
        addMethod(object_getClass(winDelegate), @selector(windowShouldClose:), (IMP)sc_windowShouldClose, "c@:@");
    }
    id appDelegate = [NSApp delegate];
    if (appDelegate) {
        Class cls = object_getClass(appDelegate);
        addMethod(cls, @selector(applicationShouldHandleReopen:hasVisibleWindows:), (IMP)sc_shouldHandleReopen, "c@:@c");
        addMethod(cls, @selector(applicationShouldTerminate:), (IMP)sc_shouldTerminate, "Q@:@");
    }

    buildMenu();
    [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
    if (setIcon && iconPNG && iconLen > 0) { // dev runs outside a .app bundle have no icon otherwise
        NSData *data = [NSData dataWithBytes:iconPNG length:iconLen];
        NSImage *img = [[NSImage alloc] initWithData:data];
        if (img) [NSApp setApplicationIconImage:img];
    }
    [scWindow setMinSize:NSMakeSize(1000, 640)];
    [scWindow center];
    [scWindow setTitlebarAppearsTransparent:NO];
    sc_show();
}

void sc_show(void) {
    if (!scWindow) return;
    if ([scWindow isMiniaturized]) [scWindow deminiaturize:nil];
    [scWindow makeKeyAndOrderFront:nil];
    if (@available(macOS 14.0, *)) {
        [NSApp activate];
    } else {
        [NSApp activateIgnoringOtherApps:YES];
    }
}

// ---- notifications ---------------------------------------------------------------------------

// Shows banners even while StreamCap is frontmost, and opens the live room when one is clicked.
@interface SCNotifyDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation SCNotifyDelegate
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))completionHandler {
    if (@available(macOS 11.0, *)) {
        completionHandler(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList |
                          UNNotificationPresentationOptionSound);
    } else { // unreachable: Info.plist requires macOS 11
        completionHandler(UNNotificationPresentationOptionSound);
    }
}

- (void)userNotificationCenter:(UNUserNotificationCenter *)center
    didReceiveNotificationResponse:(UNNotificationResponse *)response
             withCompletionHandler:(void (^)(void))completionHandler {
    NSString *url = response.notification.request.content.userInfo[@"url"];
    if (url.length > 0) {
        NSURL *u = [NSURL URLWithString:url];
        if (u) [[NSWorkspace sharedWorkspace] openURL:u];
    } else {
        dispatch_async(dispatch_get_main_queue(), ^{ sc_show(); });
    }
    completionHandler();
}
@end

static SCNotifyDelegate *scNotifyDelegate = nil;

static char *dupError(NSString *msg) { return strdup(msg.UTF8String); }

char *sc_notify(const char *title, const char *body, const char *url) {
    // UNUserNotificationCenter throws outside an .app bundle (no bundle identifier).
    if (NSBundle.mainBundle.bundleIdentifier == nil) {
        return dupError(@"not running from an .app bundle");
    }
    UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        scNotifyDelegate = [[SCNotifyDelegate alloc] init];
        center.delegate = scNotifyDelegate;
    });

    __block NSString *failure = nil;
    dispatch_semaphore_t done = dispatch_semaphore_create(0);
    // Asks only the first time; afterwards it reports the user's choice from System Settings.
    [center requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
                          completionHandler:^(BOOL granted, NSError *error) {
        if (!granted) {
            failure = error ? error.localizedDescription : @"denied";
        }
        dispatch_semaphore_signal(done);
    }];
    if (dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 60 * NSEC_PER_SEC)) != 0) {
        return dupError(@"等待通知授权超时");
    }
    if (failure) {
        return dupError([@"denied: " stringByAppendingString:failure]);
    }

    UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
    content.title = [NSString stringWithUTF8String:title];
    content.body = [NSString stringWithUTF8String:body];
    content.sound = [UNNotificationSound defaultSound];
    content.userInfo = @{@"url": [NSString stringWithUTF8String:url]};
    UNNotificationRequest *req = [UNNotificationRequest requestWithIdentifier:[[NSUUID UUID] UUIDString]
                                                                      content:content
                                                                      trigger:nil];
    [center addNotificationRequest:req withCompletionHandler:^(NSError *error) {
        if (error) failure = error.localizedDescription;
        dispatch_semaphore_signal(done);
    }];
    if (dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC)) != 0) {
        return dupError(@"发送通知超时");
    }
    return failure ? dupError(failure) : NULL;
}

// ---- hidden live-page watcher ----------------------------------------------------------------

// The page's own script produces the signature that douyin's comment websocket requires, so the
// only way to follow comments without forging it is to run the page and forward the frames it
// receives. This is a small, read-only "browser extension": the wrapper below never sends anything
// on the page's behalf, and heartbeats stay the page's business.
static NSString *const scWatcherUA =
    @"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15";

// Blocks the video segments themselves (the page still gets its APIs, avatars and scripts).
static NSString *const scNoVideoRules =
    @"[{\"trigger\":{\"url-filter\":\"\\\\.(flv|m3u8|ts|m4s)\"},\"action\":{\"type\":\"block\"}}]";

// AtDocumentStart: wrap window.WebSocket, forward frames of the comment connection as base64, and
// keep all media muted. Read-only: nothing is modified or sent.
static NSString *const scWatcherJS =
    @"(function(){\n"
    @"  var W = window.WebSocket;\n"
    @"  if (!W || W.__scWrapped) return;\n"
    @"  function b64(buf){ var b=new Uint8Array(buf), s='', n=0x8000;\n"
    @"    for (var i=0;i<b.length;i+=n) s+=String.fromCharCode.apply(null,b.subarray(i,i+n));\n"
    @"    return btoa(s); }\n"
    @"  function send(d){ try{\n"
    @"      if (d instanceof ArrayBuffer) window.webkit.messageHandlers.streamcapDanmaku.postMessage(b64(d));\n"
    @"      else if (typeof Blob!=='undefined' && d instanceof Blob)\n"
    @"        d.arrayBuffer().then(function(x){ window.webkit.messageHandlers.streamcapDanmaku.postMessage(b64(x)); });\n"
    @"    }catch(e){} }\n"
    @"  function Wrapped(url, protocols){\n"
    @"    var ws = protocols===undefined ? new W(url) : new W(url, protocols);\n"
    @"    try{ if (String(url).indexOf('/webcast/im/push/v2/')>=0)\n"
    @"      ws.addEventListener('message', function(ev){ send(ev.data); }); }catch(e){}\n"
    @"    return ws; }\n"
    @"  Wrapped.prototype = W.prototype;\n"
    @"  Wrapped.CONNECTING=W.CONNECTING; Wrapped.OPEN=W.OPEN; Wrapped.CLOSING=W.CLOSING; Wrapped.CLOSED=W.CLOSED;\n"
    @"  Wrapped.__scWrapped = true; window.WebSocket = Wrapped;\n"
    @"  function mute(m){ try{ m.muted=true; }catch(e){} }\n"
    @"  function scan(){ var l=document.querySelectorAll('video,audio'); for (var i=0;i<l.length;i++) mute(l[i]); }\n"
    @"  try{ new MutationObserver(scan).observe(document.documentElement,{childList:true,subtree:true}); }catch(e){}\n"
    @"  document.addEventListener('DOMContentLoaded', scan);\n"
    @"})();";

@interface SCWatcher : NSObject <WKScriptMessageHandler, WKNavigationDelegate>
@property (nonatomic) long long wid;
@property (nonatomic, strong) NSURL *url;
@property (nonatomic, strong) WKWebView *webView;
@property (nonatomic, strong) NSWindow *window;
@property (nonatomic, strong) NSTimer *timer;
@property (nonatomic, strong) NSDate *lastFrame;
@property (nonatomic, strong) NSDate *loadedAt;
@property (nonatomic) BOOL closed;
@end

static NSMutableDictionary<NSNumber *, SCWatcher *> *scWatchers(void) {
    static NSMutableDictionary *d;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ d = [NSMutableDictionary new]; });
    return d;
}

@implementation SCWatcher

- (void)start {
    // Compiling is cached by identifier, so concurrent watchers share one list.
    [[WKContentRuleListStore defaultStore]
        compileContentRuleListForIdentifier:@"streamcap-no-video-v1"
                     encodedContentRuleList:scNoVideoRules
                          completionHandler:^(WKContentRuleList *rules, NSError *error) {
        dispatch_async(dispatch_get_main_queue(), ^{ [self installWithRules:rules]; });
    }];
}

- (void)installWithRules:(WKContentRuleList *)rules {
    if (self.closed) return;
    WKWebViewConfiguration *cfg = [[WKWebViewConfiguration alloc] init];
    cfg.websiteDataStore = [WKWebsiteDataStore nonPersistentDataStore]; // no account state at all
    cfg.mediaTypesRequiringUserActionForPlayback = WKAudiovisualMediaTypeNone;
    if (rules) [cfg.userContentController addContentRuleList:rules];
    [cfg.userContentController addScriptMessageHandler:self name:@"streamcapDanmaku"];
    [cfg.userContentController addUserScript:[[WKUserScript alloc]
        initWithSource:scWatcherJS
         injectionTime:WKUserScriptInjectionTimeAtDocumentStart
      forMainFrameOnly:YES]];

    // Off the visible area, but ordered in and at a normal size: at size 0 (or minimized) the page
    // stops working. Borderless, transparent to the mouse, out of the window menu.
    NSRect frame = NSMakeRect(-4000, -4000, 1280, 800);
    self.window = [[NSWindow alloc] initWithContentRect:frame
                                              styleMask:NSWindowStyleMaskBorderless
                                                backing:NSBackingStoreBuffered
                                                  defer:NO];
    self.window.releasedWhenClosed = NO;
    self.window.opaque = NO;
    self.window.backgroundColor = [NSColor clearColor];
    self.window.hasShadow = NO;
    self.window.ignoresMouseEvents = YES;
    self.window.excludedFromWindowsMenu = YES;
    self.window.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorTransient;

    self.webView = [[WKWebView alloc] initWithFrame:NSMakeRect(0, 0, 1280, 800) configuration:cfg];
    self.webView.customUserAgent = scWatcherUA;
    self.webView.navigationDelegate = self;
    self.webView.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    [self.window.contentView addSubview:self.webView];
    [self.window orderFrontRegardless];
    self.loadedAt = [NSDate date];
    [self.webView loadRequest:[NSURLRequest requestWithURL:self.url]];

    // Douyin sends a heartbeat frame well within this window; a long silence means a dead page.
    self.timer = [NSTimer scheduledTimerWithTimeInterval:20 target:self selector:@selector(tick) userInfo:nil repeats:YES];
}

- (void)tick {
    if (self.closed) return;
    NSDate *since = self.lastFrame ?: self.loadedAt;
    if (since && [[NSDate date] timeIntervalSinceDate:since] > 120) {
        NSLog(@"StreamCap: no danmaku frames for 2 minutes, reloading %@", self.url);
        self.loadedAt = [NSDate date];
        [self.webView reload];
    }
}

- (void)reloadSoon {
    if (self.closed) return;
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
        if (!self.closed) {
            self.loadedAt = [NSDate date];
            [self.webView reload];
        }
    });
}

- (void)shutdown {
    if (self.closed) return;
    self.closed = YES;
    [self.timer invalidate];
    self.timer = nil;
    [self.webView.configuration.userContentController removeScriptMessageHandlerForName:@"streamcapDanmaku"];
    [self.webView stopLoading];
    self.webView.navigationDelegate = nil;
    [self.webView removeFromSuperview];
    self.webView = nil;
    [self.window orderOut:nil];
    self.window = nil;
}

// ---- WKScriptMessageHandler ----

- (void)userContentController:(WKUserContentController *)controller didReceiveScriptMessage:(WKScriptMessage *)message {
    if (self.closed) return;
    if (![message.body isKindOfClass:[NSString class]]) return;
    NSData *data = [[NSData alloc] initWithBase64EncodedString:(NSString *)message.body options:0];
    if (data.length == 0) return;
    self.lastFrame = [NSDate date];
    goDanmakuFrame(self.wid, data.bytes, (int)data.length);
}

// ---- WKNavigationDelegate ----

- (void)webView:(WKWebView *)webView didFinishNavigation:(WKNavigation *)navigation {
    self.loadedAt = [NSDate date];
}

- (void)webView:(WKWebView *)webView didFailProvisionalNavigation:(WKNavigation *)navigation withError:(NSError *)error {
    [self reloadSoon];
}

- (void)webView:(WKWebView *)webView didFailNavigation:(WKNavigation *)navigation withError:(NSError *)error {
    [self reloadSoon];
}

- (void)webViewWebContentProcessDidTerminate:(WKWebView *)webView {
    [self reloadSoon];
}

@end

void sc_watcher_open(const char *url, long long wid) {
    NSString *s = [NSString stringWithUTF8String:url];
    if (s.length == 0) return;
    dispatch_async(dispatch_get_main_queue(), ^{
        NSURL *u = [NSURL URLWithString:s];
        if (!u) return;
        SCWatcher *w = [[SCWatcher alloc] init];
        w.wid = wid;
        w.url = u;
        scWatchers()[@(wid)] = w;
        [w start];
    });
}

void sc_watcher_close(long long wid) {
    dispatch_async(dispatch_get_main_queue(), ^{
        SCWatcher *w = scWatchers()[@(wid)];
        if (!w) return;
        [scWatchers() removeObjectForKey:@(wid)];
        [w shutdown];
    });
}
