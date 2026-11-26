// NexusMac.m - the macOS system integration for the Uno head (libnexusmac.dylib).
//
// Why a native helper: Uno's Skia desktop host on macOS has no Xamarin.Mac
// bindings. Calling AppKit from C# through objc_msgSend P/Invoke works, but every
// selector and signature is then unchecked, the arm64 and x86_64 calling
// conventions differ for msgSend, and menu targets or Apple-event handlers need an
// Objective-C class built at runtime. A few hundred lines of Objective-C, compiled
// by clang in CI with ARC, avoid all of that and expose a small C ABI instead
// (see desktop/uno/Platform/MacNative.cs). When the library is missing, the app
// still runs without these features.
//
// It provides:
//   - the menu bar status item (replaces the Windows tray icon), a template image
//     of the Nexus mark drawn from frontend/public/brand/nexus-mark.svg;
//   - the standard app menu bar (Nexus, File, Edit, Window, Help);
//   - Dock reopen, nexus:// URL events, and a Quit that goes through the app's
//     own shutdown;
//   - showing and hiding the Uno window (closing hides it while the item exists);
//   - App Translocation lookup and Move to Trash, for "Move to Applications".
//
// Build (universal):
//   clang -arch arm64 -arch x86_64 -mmacosx-version-min=12.0 -fobjc-arc -dynamiclib \
//     -framework AppKit -framework CoreServices -install_name @rpath/libnexusmac.dylib \
//     -o libnexusmac.dylib NexusMac.m

#import <AppKit/AppKit.h>
#import <CoreServices/CoreServices.h>
#import <objc/runtime.h>
#import <UserNotifications/UserNotifications.h>
#include <dlfcn.h>

typedef void (*nx_command_fn)(int32_t command);
typedef void (*nx_url_fn)(const char *url);

// Keep in sync with MacNative.Command.
enum {
    NXOpen = 1,
    NXTogglePause = 2,
    NXReport = 3,
    NXQuit = 4,
    NXSettings = 5,
    NXReopen = 6,
    NXTerminateRequested = 7,
    NXCloseWindow = 8,
    NXNewChat = 9,
    NXSearch = 10,
    NXSwitchProject = 11,
    NXUndo = 12,
    NXRedo = 13,
    NXCut = 14,
    NXCopy = 15,
    NXPaste = 16,
    NXSelectAll = 17,
};

// Virtual key codes (Carbon HIToolbox kVK_ANSI_*), for menu-chosen shortcuts.
enum {
    NXKeyF = 3,
};

static nx_command_fn s_command;
static nx_url_fn s_url;
static NSStatusItem *s_item;
static NSMenuItem *s_stateItem;
static NSMenuItem *s_pauseItem;
static NSMenuItem *s_agentsItem;
static NSMenuItem *s_agentsSeparator;
static NSString *s_agentsText;
static BOOL s_paused;
static BOOL s_installed;
static BOOL s_delivering;
static IMP s_originalShouldTerminate;
static IMP s_originalTerminateAfterLastWindow;
static id s_launchObserver;

static void nx_on_main(dispatch_block_t block)
{
    if ([NSThread isMainThread]) block();
    else dispatch_async(dispatch_get_main_queue(), block);
}

static void nx_send(int32_t command)
{
    nx_command_fn cb = s_command;
    if (cb != NULL) cb(command);
}

static void nx_deliver_url(NSString *url)
{
    if (url.length == 0) return;
    nx_url_fn cb = s_url;
    if (cb != NULL) cb(url.UTF8String);
}

static BOOL nx_is_uno_window(NSWindow *window)
{
    return window != nil && [NSStringFromClass([window class]) hasPrefix:@"UNO"];
}

static NSArray<NSWindow *> *nx_uno_windows(void)
{
    NSMutableArray<NSWindow *> *found = [NSMutableArray array];
    for (NSWindow *window in NSApp.windows) {
        if (nx_is_uno_window(window)) [found addObject:window];
    }
    return found;
}

// Find (Cmd+F) belongs to whichever page is showing, so its menu item sends the
// key to Uno for the page's own KeyboardAccelerator, as if no menu had claimed
// it. Outside a Uno window (the About panel, an alert) the standard action goes
// down the responder chain.
// The event is delivered on a later turn of the main queue (the menu is still
// inside performKeyEquivalent when the action runs). If Uno leaves the key
// unhandled, UNOWindow passes it to NSWindow, which offers it to the main menu
// again: s_delivering stops that from looping.
static void nx_deliver_later(NSWindow *window, NSArray<NSEvent *> *events)
{
    dispatch_async(dispatch_get_main_queue(), ^{
        s_delivering = YES;
        @try {
            for (NSEvent *e in events) [window sendEvent:e];
        } @finally {
            s_delivering = NO;
        }
    });
}

static void nx_forward_shortcut(NSMenuItem *item)
{
    if (s_delivering) return; // Uno did not want it; do not bounce it back.
    NSWindow *key = NSApp.keyWindow;
    if (!nx_is_uno_window(key)) {
        NSString *fallback = item.representedObject;
        if ([fallback isKindOfClass:[NSString class]] && fallback.length > 0) {
            [NSApp sendAction:NSSelectorFromString(fallback) to:nil from:item];
        }
        return;
    }
    NSString *chars = item.keyEquivalent.lowercaseString;
    NSEventModifierFlags mods = item.keyEquivalentModifierMask;
    NSEvent *current = NSApp.currentEvent;
    if (current.type == NSEventTypeKeyDown
        && (current.modifierFlags & NSEventModifierFlagCommand) != 0
        && [current.charactersIgnoringModifiers.lowercaseString isEqualToString:chars]) {
        nx_deliver_later(key, @[ current ]);
        return;
    }
    // Chosen with the mouse: synthesize the key press.
    unsigned short code = (unsigned short)item.tag;
    NSTimeInterval now = [NSProcessInfo processInfo].systemUptime;
    NSString *typed = (mods & NSEventModifierFlagShift) ? chars.uppercaseString : chars;
    NSMutableArray<NSEvent *> *events = [NSMutableArray array];
    for (int i = 0; i < 2; i++) {
        NSEvent *e = [NSEvent keyEventWithType:(i == 0 ? NSEventTypeKeyDown : NSEventTypeKeyUp)
                                      location:NSZeroPoint
                                 modifierFlags:mods
                                     timestamp:now
                                  windowNumber:key.windowNumber
                                       context:nil
                                    characters:typed
                   charactersIgnoringModifiers:chars
                                     isARepeat:NO
                                       keyCode:code];
        if (e != nil) [events addObject:e];
    }
    nx_deliver_later(key, events);
}

@interface NXMacTarget : NSObject
@end

@implementation NXMacTarget
- (void)command:(NSMenuItem *)sender { nx_send((int32_t)sender.tag); }
- (void)forwardShortcut:(NSMenuItem *)sender { nx_forward_shortcut(sender); }
// Edit commands act on the focused Uno text control (managed side); in other
// windows (About panel, alerts) the standard action goes down the responder chain.
- (void)edit:(NSMenuItem *)sender
{
    if (nx_is_uno_window(NSApp.keyWindow)) {
        nx_send((int32_t)sender.tag);
        return;
    }
    NSString *fallback = sender.representedObject;
    if ([fallback isKindOfClass:[NSString class]] && fallback.length > 0) {
        [NSApp sendAction:NSSelectorFromString(fallback) to:nil from:sender];
    }
}
- (void)handleGetURL:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply
{
    nx_deliver_url([[event paramDescriptorForKeyword:keyDirectObject] stringValue]);
}
@end

static NXMacTarget *s_target;

static NXMacTarget *nx_target(void)
{
    if (s_target == nil) s_target = [[NXMacTarget alloc] init];
    return s_target;
}

static void nx_register_url_handler(void)
{
    [[NSAppleEventManager sharedAppleEventManager] setEventHandler:nx_target()
                                                       andSelector:@selector(handleGetURL:withReplyEvent:)
                                                     forEventClass:kInternetEventClass
                                                        andEventID:kAEGetURL];
}

// ---- Delegate hooks, added to Uno's UNOApplicationDelegate class -------------

static BOOL nx_should_handle_reopen(id self, SEL _cmd, NSApplication *app, BOOL hasVisibleWindows)
{
    if (!hasVisibleWindows) nx_send(NXReopen);
    return YES;
}

static void nx_open_urls(id self, SEL _cmd, NSApplication *app, NSArray<NSURL *> *urls)
{
    for (NSURL *url in urls) nx_deliver_url(url.absoluteString);
}

static BOOL nx_session_ending(void)
{
    NSAppleEventDescriptor *event = [[NSAppleEventManager sharedAppleEventManager] currentAppleEvent];
    if (event.eventClass != kCoreEventClass || event.eventID != kAEQuitApplication) return NO;
    OSType reason = [event attributeDescriptorForKeyword:kAEQuitReason].typeCodeValue;
    switch (reason) {
        case kAELogOut:
        case kAEReallyLogOut:
        case kAEShowRestartDialog:
        case kAERestart:
        case kAEShowShutdownDialog:
        case kAEShutDown:
            return YES;
        default:
            return NO;
    }
}

// Cmd+Q from elsewhere and the Dock's Quit: Uno would ask the (possibly hidden)
// window to close, which the app turns into "hide". Route it to the app's Quit
// instead, which shuts the core down and exits. Logout and restart keep Uno's
// behaviour (terminate now).
static NSString *nx_four_cc(OSType code)
{
    char c[5] = { (char)(code >> 24), (char)(code >> 16), (char)(code >> 8), (char)code, 0 };
    return [NSString stringWithUTF8String:c] ?: @"????";
}

static NSString *nx_describe_quit_source(void)
{
    NSAppleEventDescriptor *event = [[NSAppleEventManager sharedAppleEventManager] currentAppleEvent];
    if (event == nil) return [NSString stringWithFormat:@"no apple event, ui event type %ld", (long)NSApp.currentEvent.type];
    pid_t sender = [event attributeDescriptorForKeyword:keySenderPIDAttr].int32Value;
    return [NSString stringWithFormat:@"apple event %@/%@ from pid %d", nx_four_cc(event.eventClass), nx_four_cc(event.eventID), sender];
}

static NSApplicationTerminateReply nx_should_terminate(id self, SEL _cmd, NSApplication *sender)
{
    if (s_command != NULL && s_item != nil && !nx_session_ending()) {
        NSLog(@"[nexus-mac] quit requested by the system (Dock or menu): %@", nx_describe_quit_source());
        nx_send(NXTerminateRequested);
        return NSTerminateCancel;
    }
    if (s_originalShouldTerminate != NULL) {
        return ((NSApplicationTerminateReply (*)(id, SEL, NSApplication *))s_originalShouldTerminate)(self, _cmd, sender);
    }
    return NSTerminateNow;
}

// Hiding the only window counts as "last window closed" for AppKit, and Uno
// answers YES, so the app would quit the moment it is hidden. While the menu
// bar item exists the app stays; without it, Uno decides as before.
static BOOL nx_terminate_after_last_window(id self, SEL _cmd, NSApplication *sender)
{
    if (s_item != nil) return NO;
    if (s_originalTerminateAfterLastWindow != NULL) {
        return ((BOOL (*)(id, SEL, NSApplication *))s_originalTerminateAfterLastWindow)(self, _cmd, sender);
    }
    return NO;
}

static void nx_hook_delegate(void)
{
    id delegate = NSApp.delegate;
    if (delegate == nil) return;
    Class cls = object_getClass(delegate);
    char reopenTypes[16];
    snprintf(reopenTypes, sizeof reopenTypes, "%s@:@%s", @encode(BOOL), @encode(BOOL));
    class_replaceMethod(cls, @selector(applicationShouldHandleReopen:hasVisibleWindows:), (IMP)nx_should_handle_reopen, reopenTypes);
    class_replaceMethod(cls, @selector(application:openURLs:), (IMP)nx_open_urls, "v@:@@");
    char lastWindowTypes[16];
    snprintf(lastWindowTypes, sizeof lastWindowTypes, "%s@:@", @encode(BOOL));
    IMP previousLastWindow = class_replaceMethod(cls, @selector(applicationShouldTerminateAfterLastWindowClosed:),
                                                 (IMP)nx_terminate_after_last_window, lastWindowTypes);
    if (previousLastWindow != (IMP)nx_terminate_after_last_window) s_originalTerminateAfterLastWindow = previousLastWindow;
    Method terminate = class_getInstanceMethod(cls, @selector(applicationShouldTerminate:));
    if (terminate != NULL) {
        IMP previous = method_setImplementation(terminate, (IMP)nx_should_terminate);
        if (previous != (IMP)nx_should_terminate) s_originalShouldTerminate = previous;
    } else {
        char types[16];
        snprintf(types, sizeof types, "%s@:@", @encode(NSApplicationTerminateReply));
        class_addMethod(cls, @selector(applicationShouldTerminate:), (IMP)nx_should_terminate, types);
    }
    // NSApplication caches which optional delegate methods exist when the
    // delegate is set; set it again so the new ones are seen.
    NSApp.delegate = nil;
    NSApp.delegate = delegate;
}

// ---- The Nexus mark as a menu bar template image ------------------------------

static NSBezierPath *nx_stroke(CGFloat width)
{
    NSBezierPath *p = [NSBezierPath bezierPath];
    p.lineWidth = width;
    p.lineCapStyle = NSLineCapStyleRound;
    p.lineJoinStyle = NSLineJoinStyleRound;
    return p;
}

static NSImage *nx_mark_image(void)
{
    NSImage *image = [NSImage imageWithSize:NSMakeSize(18, 18) flipped:YES drawingHandler:^BOOL(NSRect rect) {
        // nexus-mark.svg uses a 64-unit box; its strokes span 9..55.
        NSAffineTransform *t = [NSAffineTransform transform];
        [t translateXBy:1 yBy:1];
        [t scaleBy:16.0 / 46.0];
        [t translateXBy:-9 yBy:-9];
        [t concat];
        const CGFloat w = 7, gap = 5;

        NSBezierPath *left = nx_stroke(w);       // M14 47V20a6 6 0 0 1 6-6h5l25 33
        [left moveToPoint:NSMakePoint(14, 47)];
        [left appendBezierPathWithArcFromPoint:NSMakePoint(14, 14) toPoint:NSMakePoint(20, 14) radius:6];
        [left lineToPoint:NSMakePoint(25, 14)];
        [left lineToPoint:NSMakePoint(50, 47)];

        NSBezierPath *right = nx_stroke(w);      // M50 17v27a6 6 0 0 1-6 6h-5L14 17
        [right moveToPoint:NSMakePoint(50, 17)];
        [right appendBezierPathWithArcFromPoint:NSMakePoint(50, 50) toPoint:NSMakePoint(44, 50) radius:6];
        [right lineToPoint:NSMakePoint(39, 50)];
        [right lineToPoint:NSMakePoint(14, 17)];

        NSBezierPath *link = nx_stroke(w);       // m25 32 8 10
        [link moveToPoint:NSMakePoint(25, 32)];
        [link lineToPoint:NSMakePoint(33, 42)];

        NSGraphicsContext *g = [NSGraphicsContext currentContext];
        [[NSColor blackColor] setStroke];
        [left stroke];
        // One colour only, so each stroke that passes over another first cuts a
        // thin gap: the crossing still reads as a weave.
        for (NSBezierPath *over in @[ right, link ]) {
            g.compositingOperation = NSCompositingOperationClear;
            over.lineWidth = w + gap;
            [over stroke];
            g.compositingOperation = NSCompositingOperationSourceOver;
            over.lineWidth = w;
            [over stroke];
        }
        return YES;
    }];
    image.template = YES;
    image.accessibilityDescription = @"Nexus";
    return image;
}

// ---- Menus --------------------------------------------------------------------

static NSMenuItem *nx_item(NSMenu *menu, NSString *title, SEL action, id target, NSString *key, NSEventModifierFlags mods)
{
    NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key ?: @""];
    item.target = target;
    if (key.length > 0) item.keyEquivalentModifierMask = mods;
    [menu addItem:item];
    return item;
}

static NSMenuItem *nx_command_item(NSMenu *menu, NSString *title, int32_t command, NSString *key, NSEventModifierFlags mods)
{
    NSMenuItem *item = nx_item(menu, title, @selector(command:), nx_target(), key, mods);
    item.tag = command;
    return item;
}

static NSMenuItem *nx_shortcut_item(NSMenu *menu, NSString *title, NSString *key, NSEventModifierFlags mods,
                                    unsigned short keyCode, NSString *fallbackAction)
{
    NSMenuItem *item = nx_item(menu, title, @selector(forwardShortcut:), nx_target(), key, mods);
    item.tag = keyCode;
    item.representedObject = fallbackAction;
    return item;
}

static NSMenuItem *nx_edit_item(NSMenu *menu, NSString *title, int32_t command, NSString *key,
                                NSEventModifierFlags mods, NSString *fallbackAction)
{
    NSMenuItem *item = nx_item(menu, title, @selector(edit:), nx_target(), key, mods);
    item.tag = command;
    item.representedObject = fallbackAction;
    return item;
}

static NSMenu *nx_submenu(NSMenu *bar, NSString *title)
{
    NSMenuItem *holder = [[NSMenuItem alloc] initWithTitle:title action:nil keyEquivalent:@""];
    NSMenu *menu = [[NSMenu alloc] initWithTitle:title];
    holder.submenu = menu;
    [bar addItem:holder];
    return menu;
}

static void nx_build_main_menu(void)
{
    const NSEventModifierFlags cmd = NSEventModifierFlagCommand;
    NSMenu *bar = [[NSMenu alloc] initWithTitle:@"MainMenu"];

    NSMenu *app = nx_submenu(bar, @"Nexus");
    nx_item(app, @"About Nexus", @selector(orderFrontStandardAboutPanel:), NSApp, nil, 0);
    [app addItem:[NSMenuItem separatorItem]];
    nx_command_item(app, @"Settings…", NXSettings, @",", cmd);
    [app addItem:[NSMenuItem separatorItem]];
    NSMenu *services = [[NSMenu alloc] initWithTitle:@"Services"];
    nx_item(app, @"Services", nil, nil, nil, 0).submenu = services;
    NSApp.servicesMenu = services;
    [app addItem:[NSMenuItem separatorItem]];
    nx_item(app, @"Hide Nexus", @selector(hide:), NSApp, @"h", cmd);
    nx_item(app, @"Hide Others", @selector(hideOtherApplications:), NSApp, @"h", cmd | NSEventModifierFlagOption);
    nx_item(app, @"Show All", @selector(unhideAllApplications:), NSApp, nil, 0);
    [app addItem:[NSMenuItem separatorItem]];
    nx_command_item(app, @"Quit Nexus", NXQuit, @"q", cmd);

    NSMenu *file = nx_submenu(bar, @"File");
    nx_command_item(file, @"New Chat", NXNewChat, @"n", cmd);
    nx_command_item(file, @"Search…", NXSearch, @"k", cmd);
    nx_command_item(file, @"Switch Project…", NXSwitchProject, @"p", cmd);
    [file addItem:[NSMenuItem separatorItem]];
    nx_command_item(file, @"Close Window", NXCloseWindow, @"w", cmd);

    NSMenu *edit = nx_submenu(bar, @"Edit");
    nx_edit_item(edit, @"Undo", NXUndo, @"z", cmd, @"undo:");
    nx_edit_item(edit, @"Redo", NXRedo, @"z", cmd | NSEventModifierFlagShift, @"redo:");
    [edit addItem:[NSMenuItem separatorItem]];
    nx_edit_item(edit, @"Cut", NXCut, @"x", cmd, @"cut:");
    nx_edit_item(edit, @"Copy", NXCopy, @"c", cmd, @"copy:");
    nx_edit_item(edit, @"Paste", NXPaste, @"v", cmd, @"paste:");
    nx_edit_item(edit, @"Select All", NXSelectAll, @"a", cmd, @"selectAll:");
    [edit addItem:[NSMenuItem separatorItem]];
    nx_shortcut_item(edit, @"Find", @"f", cmd, NXKeyF, @"performTextFinderAction:");

    NSMenu *window = nx_submenu(bar, @"Window");
    nx_item(window, @"Minimize", @selector(performMiniaturize:), nil, @"m", cmd);
    nx_item(window, @"Zoom", @selector(performZoom:), nil, nil, 0);
    [window addItem:[NSMenuItem separatorItem]];
    nx_item(window, @"Bring All to Front", @selector(arrangeInFront:), NSApp, nil, 0);
    NSApp.windowsMenu = window;

    NSMenu *help = nx_submenu(bar, @"Help");
    nx_command_item(help, @"Report a Problem…", NXReport, nil, 0);
    NSApp.helpMenu = help;

    NSApp.mainMenu = bar;
}

static void nx_apply_capture_state(void)
{
    if (s_item == nil) return;
    s_stateItem.title = s_paused ? @"Capture paused" : @"Capturing";
    s_pauseItem.title = s_paused ? @"Resume capture" : @"Pause capture";
    s_item.button.appearsDisabled = s_paused;
    s_item.button.toolTip = s_paused ? @"Nexus · capture paused" : @"Nexus · capturing";
    if (s_agentsText.length > 0) s_item.button.toolTip = [@"Nexus · " stringByAppendingString:s_agentsText];
}

static void nx_build_status_item(void)
{
    s_item = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
    s_item.button.image = nx_mark_image();
    s_item.button.imagePosition = NSImageOnly;
    s_item.button.accessibilityTitle = @"Nexus";

    NSMenu *menu = [[NSMenu alloc] initWithTitle:@"Nexus"];
    menu.autoenablesItems = NO;
    // "2 agents working · 1 needs you", shown while agents work.
    s_agentsItem = nx_command_item(menu, @"", NXOpen, nil, 0);
    s_agentsItem.hidden = YES;
    s_agentsSeparator = [NSMenuItem separatorItem];
    s_agentsSeparator.hidden = YES;
    [menu addItem:s_agentsSeparator];
    nx_command_item(menu, @"Open Nexus", NXOpen, nil, 0);
    [menu addItem:[NSMenuItem separatorItem]];
    s_stateItem = nx_item(menu, @"Capturing", nil, nil, nil, 0);
    s_stateItem.enabled = NO;
    s_pauseItem = nx_command_item(menu, @"Pause capture", NXTogglePause, nil, 0);
    [menu addItem:[NSMenuItem separatorItem]];
    nx_command_item(menu, @"Report a problem…", NXReport, nil, 0);
    [menu addItem:[NSMenuItem separatorItem]];
    nx_command_item(menu, @"Quit Nexus", NXQuit, nil, 0);
    s_item.menu = menu;
    nx_apply_capture_state();

    // Logged once the menu bar has laid the item out (CI looks for this line).
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(1.5 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
        NSWindow *w = s_item.button.window;
        NSLog(@"[nexus-mac] status item ready visible=%d frame=%@", s_item.visible ? 1 : 0,
              w != nil ? NSStringFromRect(w.frame) : @"(none)");
    });
}

// ---- C ABI ---------------------------------------------------------------------

__attribute__((visibility("default")))
void nx_mac_listen_urls(nx_url_fn callback)
{
    s_url = callback;
    nx_on_main(^{
        nx_register_url_handler();
        // AppKit installs its own handlers while it finishes launching; Apple's
        // documented time to register is applicationWillFinishLaunching.
        if (s_launchObserver == nil) {
            s_launchObserver = [[NSNotificationCenter defaultCenter]
                addObserverForName:NSApplicationWillFinishLaunchingNotification
                            object:nil
                             queue:nil
                        usingBlock:^(NSNotification *note) { nx_register_url_handler(); }];
        }
    });
}

__attribute__((visibility("default")))
bool nx_mac_install(nx_command_fn callback)
{
    if (![NSThread isMainThread]) {
        __block bool result = false;
        dispatch_sync(dispatch_get_main_queue(), ^{ result = nx_mac_install(callback); });
        return result;
    }
    s_command = callback;
    if (s_installed) return true;
    if (NSApp == nil || NSApp.delegate == nil) return false;
    nx_hook_delegate();
    nx_build_main_menu();
    nx_build_status_item();
    // The launch may have registered before AppKit finished launching.
    nx_register_url_handler();
    s_installed = YES;
    NSLog(@"[nexus-mac] installed: status item, app menu, reopen, url events");
    return true;
}

__attribute__((visibility("default")))
void nx_mac_set_capture_state(int32_t paused)
{
    nx_on_main(^{
        s_paused = paused != 0;
        nx_apply_capture_state();
    });
}

__attribute__((visibility("default")))
void nx_mac_show_windows(void)
{
    nx_on_main(^{
        if (@available(macOS 14.0, *)) [NSApp activate];
        else [NSApp activateIgnoringOtherApps:YES];
        NSArray<NSWindow *> *windows = nx_uno_windows();
        for (NSWindow *window in windows) {
            if (window.miniaturized) [window deminiaturize:nil];
            [window makeKeyAndOrderFront:nil];
        }
        NSLog(@"[nexus-mac] show: %lu window(s)", (unsigned long)windows.count);
    });
}

__attribute__((visibility("default")))
void nx_mac_hide_windows(void)
{
    nx_on_main(^{
        NSArray<NSWindow *> *windows = nx_uno_windows();
        for (NSWindow *window in windows) [window orderOut:nil];
        NSLog(@"[nexus-mac] hide: %lu window(s); status item visible=%d", (unsigned long)windows.count,
              s_item != nil && s_item.visible ? 1 : 0);
    });
}

__attribute__((visibility("default")))
int32_t nx_mac_status_item_visible(void)
{
    return s_item != nil && s_item.visible ? 1 : 0;
}

// ---- Install location ("Move to Applications")

// Gatekeeper runs a quarantined app that was never moved from where it was
// downloaded from a random read-only copy (App Translocation). Security.framework
// can say so and name the original; the calls are SPI, so they are looked up at
// run time and their absence means "not translocated".
// Returns 1 when `path` is translocated (and writes the original bundle path to
// `out`, or "" when it cannot be found), 0 otherwise.
__attribute__((visibility("default")))
int32_t nx_mac_original_path(const char *path, char *out, int32_t len)
{
    if (out != NULL && len > 0) out[0] = 0;
    if (path == NULL) return 0;
    @autoreleasepool {
        void *sec = dlopen("/System/Library/Frameworks/Security.framework/Security", RTLD_LAZY);
        if (sec == NULL) return 0;
        Boolean (*isTranslocated)(CFURLRef, bool *, CFErrorRef *) = dlsym(sec, "SecTranslocateIsTranslocatedURL");
        CFURLRef (*original)(CFURLRef, CFErrorRef *) = dlsym(sec, "SecTranslocateCreateOriginalPathForURL");
        if (isTranslocated == NULL) return 0;
        NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
        bool translocated = false;
        if (!isTranslocated((__bridge CFURLRef)url, &translocated, NULL) || !translocated) return 0;
        if (original != NULL && out != NULL && len > 0) {
            CFURLRef found = original((__bridge CFURLRef)url, NULL);
            if (found != NULL) {
                NSString *p = [(__bridge_transfer NSURL *)found path];
                if (p != nil) strlcpy(out, p.fileSystemRepresentation, (size_t)len);
            }
        }
        return 1;
    }
}

// Moves `path` to the Trash (so a replaced copy can be recovered). 1 on success.
__attribute__((visibility("default")))
int32_t nx_mac_trash(const char *path)
{
    if (path == NULL) return 0;
    @autoreleasepool {
        NSError *error = nil;
        NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
        BOOL ok = [[NSFileManager defaultManager] trashItemAtURL:url resultingItemURL:nil error:&error];
        if (!ok) NSLog(@"[nexus-mac] trash failed: %@", error.localizedDescription);
        return ok ? 1 : 0;
    }
}

// ---- Agents working in the background -----------------------------------------
// The menu bar item shows how many agents work; a notification says when a
// chat finishes or needs approval while Nexus isn't focused. Clicking it opens
// that chat through the nexus:// link handler.

#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
@interface NXNotifyDelegate : NSObject <UNUserNotificationCenterDelegate, NSUserNotificationCenterDelegate>
@end

@implementation NXNotifyDelegate
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))completionHandler
{
    if (@available(macOS 11.0, *)) completionHandler(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionSound);
    else completionHandler(UNNotificationPresentationOptionAlert | UNNotificationPresentationOptionSound);
}
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
didReceiveNotificationResponse:(UNNotificationResponse *)response
         withCompletionHandler:(void (^)(void))completionHandler
{
    NSString *url = response.notification.request.content.userInfo[@"url"];
    if ([url isKindOfClass:[NSString class]]) nx_deliver_url(url);
    completionHandler();
}
- (void)userNotificationCenter:(NSUserNotificationCenter *)center didActivateNotification:(NSUserNotification *)notification
{
    NSString *url = notification.userInfo[@"url"];
    if ([url isKindOfClass:[NSString class]]) nx_deliver_url(url);
}
- (BOOL)userNotificationCenter:(NSUserNotificationCenter *)center shouldPresentNotification:(NSUserNotification *)notification
{
    return YES;
}
@end

static NXNotifyDelegate *s_notify;
static BOOL s_notifyAsked;
static BOOL s_notifyAllowed;

static void nx_legacy_notify(NSString *title, NSString *body, NSString *url)
{
    NSUserNotification *note = [[NSUserNotification alloc] init];
    note.title = title;
    note.informativeText = body;
    note.userInfo = url.length > 0 ? @{ @"url": url } : @{};
    NSUserNotificationCenter *center = [NSUserNotificationCenter defaultUserNotificationCenter];
    center.delegate = s_notify;
    [center deliverNotification:note];
}

static void nx_post(NSString *title, NSString *body, NSString *url)
{
    if (!s_notifyAllowed) {
        nx_legacy_notify(title, body, url);
        return;
    }
    UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
    content.title = title;
    content.body = body;
    content.sound = [UNNotificationSound defaultSound];
    content.userInfo = url.length > 0 ? @{ @"url": url } : @{};
    UNNotificationRequest *request = [UNNotificationRequest requestWithIdentifier:[[NSUUID UUID] UUIDString] content:content trigger:nil];
    [[UNUserNotificationCenter currentNotificationCenter] addNotificationRequest:request withCompletionHandler:^(NSError *error) {
        if (error != nil) {
            NSLog(@"[nexus-mac] notification failed (%@); using the legacy center", error.localizedDescription);
            dispatch_async(dispatch_get_main_queue(), ^{ nx_legacy_notify(title, body, url); });
        }
    }];
}
#pragma clang diagnostic pop

__attribute__((visibility("default")))
void nx_mac_notify(const char *title, const char *body, const char *url)
{
    NSString *t = title != NULL ? [NSString stringWithUTF8String:title] : @"Nexus";
    NSString *b = body != NULL ? [NSString stringWithUTF8String:body] : @"";
    NSString *u = url != NULL ? [NSString stringWithUTF8String:url] : @"";
    nx_on_main(^{
        if (s_notify == nil) s_notify = [[NXNotifyDelegate alloc] init];
        if (s_notifyAsked) {
            nx_post(t ?: @"Nexus", b ?: @"", u ?: @"");
            return;
        }
        s_notifyAsked = YES;
        // An ad hoc signed app may not get UserNotifications; then the older
        // center still shows the banner.
        UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
        center.delegate = s_notify;
        [center requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
                              completionHandler:^(BOOL granted, NSError *error) {
            dispatch_async(dispatch_get_main_queue(), ^{
                s_notifyAllowed = granted && error == nil;
                NSLog(@"[nexus-mac] notifications %@", s_notifyAllowed ? @"allowed" : @"unavailable; using the legacy center");
                nx_post(t ?: @"Nexus", b ?: @"", u ?: @"");
            });
        }];
    });
}

__attribute__((visibility("default")))
void nx_mac_set_agents(const char *summary)
{
    NSString *text = summary != NULL ? [NSString stringWithUTF8String:summary] : @"";
    nx_on_main(^{
        s_agentsText = text ?: @"";
        if (s_agentsItem == nil) return;
        s_agentsItem.title = s_agentsText;
        s_agentsItem.hidden = s_agentsText.length == 0;
        s_agentsSeparator.hidden = s_agentsItem.hidden;
        nx_apply_capture_state();
    });
}
