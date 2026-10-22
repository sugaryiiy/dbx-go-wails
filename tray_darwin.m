//go:build (desktop || bindings) && darwin

#import <Cocoa/Cocoa.h>

// Go callbacks exported from tray_darwin.go.
void dbxTrayShowMainWindow(void);
void dbxTrayRequestQuit(void);

@interface DBXTrayTarget : NSObject
- (void)showMainWindow:(id)sender;
- (void)requestQuit:(id)sender;
@end

@implementation DBXTrayTarget
- (void)showMainWindow:(id)sender {
    dbxTrayShowMainWindow();
}
- (void)requestQuit:(id)sender {
    dbxTrayRequestQuit();
}
@end

static NSStatusItem *dbxStatusItem = nil;
static DBXTrayTarget *dbxTrayTarget = nil;

void dbx_tray_install(const void *iconPNG, int iconLen) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (dbxStatusItem != nil) {
            return;
        }
        dbxTrayTarget = [[DBXTrayTarget alloc] init];
        // statusItemWithLength: returns an autoreleased item that the
        // status bar does not retain; this file is compiled without ARC
        // (cgo default), so retain it explicitly for the process lifetime.
        // Without the retain the item is freed at the next autorelease-pool
        // drain and dbxStatusItem dangles into reused memory.
        dbxStatusItem = [[[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength] retain];
        NSImage *image = nil;
        if (iconPNG != NULL && iconLen > 0) {
            NSData *data = [NSData dataWithBytes:iconPNG length:(NSUInteger)iconLen];
            image = [[NSImage alloc] initWithData:data];
        }
        if (image != nil) {
            [image setSize:NSMakeSize(18, 18)];
            dbxStatusItem.button.image = image;
        } else {
            dbxStatusItem.button.title = @"MagpieDB";
        }
        NSMenu *menu = [[NSMenu alloc] init];
        NSMenuItem *showItem = [[NSMenuItem alloc] initWithTitle:@"打开 MagpieDB"
                                                          action:@selector(showMainWindow:)
                                                   keyEquivalent:@""];
        showItem.target = dbxTrayTarget;
        [menu addItem:showItem];
        [menu addItem:[NSMenuItem separatorItem]];
        NSMenuItem *quitItem = [[NSMenuItem alloc] initWithTitle:@"退出 MagpieDB"
                                                          action:@selector(requestQuit:)
                                                   keyEquivalent:@""];
        quitItem.target = dbxTrayTarget;
        [menu addItem:quitItem];
        dbxStatusItem.menu = menu;
    });
}

void dbx_tray_set_visible(int visible) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (dbxStatusItem == nil) {
            return;
        }
        dbxStatusItem.visible = visible != 0;
    });
}
