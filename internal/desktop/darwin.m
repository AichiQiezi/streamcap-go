//go:build darwin

#import <Cocoa/Cocoa.h>
#import <UserNotifications/UserNotifications.h>
#import <objc/runtime.h>
#include "darwin.h"

extern void goQuitRequested(void);

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
