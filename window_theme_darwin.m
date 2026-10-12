//go:build (desktop || bindings) && darwin

#import <Cocoa/Cocoa.h>

void dbx_set_window_theme(int theme) {
    dispatch_async(dispatch_get_main_queue(), ^{
        NSAppearance *appearance = nil;
        if (theme == 1) {
            appearance = [NSAppearance appearanceNamed:NSAppearanceNameAqua];
        } else if (theme == 2) {
            appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
        }
        // nil restores live system appearance rather than fixing the current
        // system color, so subsequent macOS theme changes continue to apply.
        [NSApp setAppearance:appearance];
    });
}
