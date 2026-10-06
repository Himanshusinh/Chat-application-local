// Native macOS shell: a real Cocoa window hosting the chat UI in WKWebView,
// with Dock icon + badge, menu bar (so Cmd+C/V/Q work), notifications, and
// native file/folder pickers for the web page.
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <UserNotifications/UserNotifications.h>
#include "_cgo_export.h"

@interface OCApp : NSObject <NSApplicationDelegate, NSWindowDelegate, WKUIDelegate, WKNavigationDelegate,
                             WKScriptMessageHandler, UNUserNotificationCenterDelegate>
@property(strong) NSWindow *window;
@property(strong) WKWebView *web;
@property(copy) NSString *url;
@property BOOL startHidden;
@property BOOL visible;
@end

static OCApp *gApp;

static BOOL hasBundle(void) { return [[NSBundle mainBundle] bundleIdentifier] != nil; }

@implementation OCApp

- (void)applicationDidFinishLaunching:(NSNotification *)note {
  [self buildMenu];
  NSRect frame = NSMakeRect(0, 0, 1180, 780);
  NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                            NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
  self.window = [[NSWindow alloc] initWithContentRect:frame styleMask:style backing:NSBackingStoreBuffered defer:NO];
  self.window.title = @"OfficeChat";
  self.window.minSize = NSMakeSize(560, 440);
  self.window.delegate = self;
  self.window.releasedWhenClosed = NO;
  self.window.backgroundColor = NSColor.whiteColor;
  [self.window center];
  [self.window setFrameAutosaveName:@"OfficeChatMainWindow"];

  WKWebViewConfiguration *cfg = [WKWebViewConfiguration new];
  [cfg.userContentController addScriptMessageHandler:self name:@"oc"];
  self.web = [[WKWebView alloc] initWithFrame:frame configuration:cfg];
  self.web.UIDelegate = self;
  self.web.navigationDelegate = self;
  self.web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
  self.window.contentView = self.web;
  [self.web loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:self.url]]];

  if (hasBundle()) {
    UNUserNotificationCenter *c = [UNUserNotificationCenter currentNotificationCenter];
    c.delegate = self;
    [c requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
                     completionHandler:^(BOOL granted, NSError *err){}];
  }
  if (!self.startHidden) [self show];
}

- (void)buildMenu {
  NSMenu *bar = [NSMenu new];

  NSMenuItem *appItem = [NSMenuItem new];
  NSMenu *appMenu = [NSMenu new];
  [appMenu addItemWithTitle:@"About OfficeChat" action:@selector(orderFrontStandardAboutPanel:) keyEquivalent:@""];
  [appMenu addItem:[NSMenuItem separatorItem]];
  NSMenuItem *settings = [appMenu addItemWithTitle:@"Settings…" action:@selector(openSettings:) keyEquivalent:@","];
  settings.target = self;
  [appMenu addItem:[NSMenuItem separatorItem]];
  [appMenu addItemWithTitle:@"Hide OfficeChat" action:@selector(hide:) keyEquivalent:@"h"];
  [appMenu addItem:[NSMenuItem separatorItem]];
  [appMenu addItemWithTitle:@"Quit OfficeChat" action:@selector(terminate:) keyEquivalent:@"q"];
  appItem.submenu = appMenu;
  [bar addItem:appItem];

  NSMenuItem *editItem = [NSMenuItem new];
  NSMenu *edit = [[NSMenu alloc] initWithTitle:@"Edit"];
  [edit addItemWithTitle:@"Undo" action:@selector(undo:) keyEquivalent:@"z"];
  NSMenuItem *redo = [edit addItemWithTitle:@"Redo" action:@selector(redo:) keyEquivalent:@"z"];
  redo.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagShift;
  [edit addItem:[NSMenuItem separatorItem]];
  [edit addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
  [edit addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
  [edit addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
  [edit addItemWithTitle:@"Select All" action:@selector(selectAll:) keyEquivalent:@"a"];
  editItem.submenu = edit;
  [bar addItem:editItem];

  NSMenuItem *winItem = [NSMenuItem new];
  NSMenu *win = [[NSMenu alloc] initWithTitle:@"Window"];
  [win addItemWithTitle:@"Minimize" action:@selector(performMiniaturize:) keyEquivalent:@"m"];
  [win addItemWithTitle:@"Close" action:@selector(performClose:) keyEquivalent:@"w"];
  winItem.submenu = win;
  [bar addItem:winItem];

  NSApp.mainMenu = bar;
  NSApp.windowsMenu = win;
}

- (void)openSettings:(id)sender {
  [self show];
  [self eval:@"window.ocNative && ocNative.openSettings()"];
}

- (void)eval:(NSString *)js {
  [self.web evaluateJavaScript:js completionHandler:nil];
}

- (void)setVisibleState:(BOOL)v {
  if (self.visible == v) return;
  self.visible = v;
  [self eval:[NSString stringWithFormat:@"window.ocNative && ocNative.setVisible(%@)", v ? @"true" : @"false"]];
  if (!v) goWindowHidden(); // install a downloaded update once the window is closed
}

- (void)show {
  if (self.window.miniaturized) [self.window deminiaturize:nil];
  [self.window makeKeyAndOrderFront:nil];
  [NSApp activateIgnoringOtherApps:YES];
  [self setVisibleState:YES];
}

// Closing the window keeps OfficeChat running (like Slack/Teams); Dock icon brings it back.
- (BOOL)windowShouldClose:(NSWindow *)w {
  [w orderOut:nil];
  [self setVisibleState:NO];
  return NO;
}
- (void)windowDidMiniaturize:(NSNotification *)n { [self setVisibleState:NO]; }
- (void)windowDidDeminiaturize:(NSNotification *)n { [self setVisibleState:YES]; }
- (void)windowDidBecomeKey:(NSNotification *)n { [self setVisibleState:YES]; }

- (BOOL)applicationShouldHandleReopen:(NSApplication *)a hasVisibleWindows:(BOOL)flag {
  [self show];
  return YES;
}
- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)a { return NO; }
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)sender {
  goWillQuit();
  return NSTerminateNow;
}

// <input type=file> and folder pickers.
- (void)webView:(WKWebView *)webView runOpenPanelWithParameters:(WKOpenPanelParameters *)p
    initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(NSArray<NSURL *> *))done {
  NSOpenPanel *panel = [NSOpenPanel openPanel];
  panel.allowsMultipleSelection = p.allowsMultipleSelection;
  panel.canChooseDirectories = p.allowsDirectories;
  panel.canChooseFiles = !p.allowsDirectories;
  panel.prompt = @"Send";
  [panel beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse r) {
    done(r == NSModalResponseOK ? panel.URLs : nil);
  }];
}

// confirm() / alert() from the page.
- (void)webView:(WKWebView *)webView runJavaScriptConfirmPanelWithMessage:(NSString *)message
    initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(BOOL))done {
  NSAlert *a = [NSAlert new];
  a.messageText = message;
  [a addButtonWithTitle:@"OK"];
  [a addButtonWithTitle:@"Cancel"];
  [a beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse r) {
    done(r == NSAlertFirstButtonReturn);
  }];
}
- (void)webView:(WKWebView *)webView runJavaScriptAlertPanelWithMessage:(NSString *)message
    initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(void))done {
  NSAlert *a = [NSAlert new];
  a.messageText = message;
  [a beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse r) { done(); }];
}

// Links open in the default browser, never inside the app window.
- (void)webView:(WKWebView *)webView decidePolicyForNavigationAction:(WKNavigationAction *)action
    decisionHandler:(void (^)(WKNavigationActionPolicy))decide {
  NSURL *u = action.request.URL;
  BOOL local = [u.host isEqualToString:@"127.0.0.1"] || [u.scheme isEqualToString:@"about"] || [u.scheme isEqualToString:@"blob"];
  if (!local && action.navigationType == WKNavigationTypeLinkActivated) {
    [[NSWorkspace sharedWorkspace] openURL:u];
    decide(WKNavigationActionPolicyCancel);
    return;
  }
  decide(local ? WKNavigationActionPolicyAllow : WKNavigationActionPolicyCancel);
  if (!local) [[NSWorkspace sharedWorkspace] openURL:u];
}
- (WKWebView *)webView:(WKWebView *)webView createWebViewWithConfiguration:(WKWebViewConfiguration *)c
    forNavigationAction:(WKNavigationAction *)action windowFeatures:(WKWindowFeatures *)f {
  if (action.request.URL) [[NSWorkspace sharedWorkspace] openURL:action.request.URL];
  return nil;
}

- (void)userContentController:(WKUserContentController *)ucc didReceiveScriptMessage:(WKScriptMessage *)m {
  if ([m.body isKindOfClass:[NSString class]]) goNativeMessage((char *)[(NSString *)m.body UTF8String]);
}

- (void)userNotificationCenter:(UNUserNotificationCenter *)c willPresentNotification:(UNNotification *)n
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))done {
  done(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList);
}
- (void)userNotificationCenter:(UNUserNotificationCenter *)c didReceiveNotificationResponse:(UNNotificationResponse *)r
         withCompletionHandler:(void (^)(void))done {
  NSString *chat = r.notification.request.content.userInfo[@"chat"];
  [self show];
  if (chat) {
    NSData *j = [NSJSONSerialization dataWithJSONObject:@[ chat ] options:0 error:nil];
    NSString *arr = [[NSString alloc] initWithData:j encoding:NSUTF8StringEncoding];
    [self eval:[NSString stringWithFormat:@"window.ocNative && ocNative.openChat(%@[0])", arr]];
  }
  done();
}
@end

static NSString *str(const char *s) { return s ? [NSString stringWithUTF8String:s] : @""; }

void ocRun(const char *url, int hidden) {
  @autoreleasepool {
    [NSApplication sharedApplication];
    [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
    gApp = [OCApp new];
    gApp.url = str(url);
    gApp.startHidden = hidden != 0;
    NSApp.delegate = gApp;
    [NSApp run];
  }
}

void ocShow(void) {
  dispatch_async(dispatch_get_main_queue(), ^{ [gApp show]; });
}

void ocQuit(void) {
  dispatch_async(dispatch_get_main_queue(), ^{ [NSApp terminate:nil]; });
}

void ocEval(const char *js) {
  NSString *s = str(js);
  dispatch_async(dispatch_get_main_queue(), ^{ [gApp eval:s]; });
}

void ocBadge(const char *label) {
  NSString *s = str(label);
  dispatch_async(dispatch_get_main_queue(), ^{ NSApp.dockTile.badgeLabel = s.length ? s : nil; });
}

void ocNotify(const char *title, const char *body, const char *chat) {
  NSString *t = str(title), *b = str(body), *c = str(chat);
  dispatch_async(dispatch_get_main_queue(), ^{
    // Bounce the Dock icon only. Do not order the window forward.
    [NSApp requestUserAttention:NSInformationalRequest];
    if (!hasBundle()) return;
    UNMutableNotificationContent *content = [UNMutableNotificationContent new];
    content.title = t;
    content.body = b;
    content.threadIdentifier = c;
    content.userInfo = @{@"chat" : c};
    UNNotificationRequest *req = [UNNotificationRequest requestWithIdentifier:[[NSUUID UUID] UUIDString]
                                                                      content:content trigger:nil];
    [[UNUserNotificationCenter currentNotificationCenter] addNotificationRequest:req withCompletionHandler:nil];
  });
}

int ocVisible(void) { return gApp && gApp.visible; }

void ocTheme(int mode) {
  dispatch_async(dispatch_get_main_queue(), ^{
    NSAppearance *a = mode == 0 ? nil : [NSAppearance appearanceNamed:(mode == 2 ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua)];
    NSApp.appearance = a;
    BOOL dark = mode == 2 || (mode == 0 && [[NSApp.effectiveAppearance bestMatchFromAppearancesWithNames:@[ NSAppearanceNameAqua, NSAppearanceNameDarkAqua ]] isEqualToString:NSAppearanceNameDarkAqua]);
    NSColor *bg = dark ? [NSColor colorWithSRGBRed:13 / 255.0 green:16 / 255.0 blue:21 / 255.0 alpha:1] : NSColor.whiteColor;
    gApp.window.backgroundColor = bg;
    if (@available(macOS 12.0, *)) gApp.web.underPageBackgroundColor = bg;
  });
}
