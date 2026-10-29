// Loaded only by test_lifecycle.py into its isolated test process. Exercises
// Cocoa's actual actions without accessibility permissions or screen automation.
#import <Cocoa/Cocoa.h>
#include <unistd.h>

__attribute__((constructor)) static void testLifecycle(void) {
    if (strcmp(getprogname(), "neutralino") != 0) return;
    FILE *pidFile = fopen(getenv("SNOOT_TEST_PID_FILE"), "w");
    if (!pidFile) _Exit(92);
    fprintf(pidFile, "%d", getpid());
    fclose(pidFile);
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
        NSWindow *window = NSApp.keyWindow ?: NSApp.windows.firstObject;
        if (!window) { fprintf(stderr, "TEST: no native window\n"); _Exit(90); }
        const char *action = getenv("SNOOT_TEST_CLOSE");
        fprintf(stderr, "TEST: %s\n", action);
        if (strcmp(action, "close-key") == 0 || strcmp(action, "quit-key") == 0) {
            NSString *key = strcmp(action, "close-key") == 0 ? @"w" : @"q";
            NSEvent *event = [NSEvent keyEventWithType:NSEventTypeKeyDown location:NSZeroPoint
                modifierFlags:NSEventModifierFlagCommand timestamp:0 windowNumber:window.windowNumber
                context:nil characters:key charactersIgnoringModifiers:key isARepeat:NO keyCode:0];
            if (![NSApp.mainMenu performKeyEquivalent:event]) {
                fprintf(stderr, "TEST: missing native shortcut\n"); _Exit(91);
            }
        } else if (strcmp(action, "close-button") == 0) {
            [[window standardWindowButton:NSWindowCloseButton] performClick:nil];
        } else if (strcmp(action, "close-command") == 0) {
            [NSApp sendAction:@selector(performClose:) to:nil from:nil];
        } else if (strcmp(action, "quit") == 0) {
            [NSApp terminate:nil];
        } else if (strcmp(action, "repeated-close") == 0) {
            [window performClose:nil];
            [window performClose:nil];
            [NSApp terminate:nil];
        } else if (strcmp(action, "parent-term") == 0) {
            kill(getppid(), SIGTERM);
        } else if (strcmp(action, "signal") == 0) {
            kill(getpid(), SIGTERM);
        }
    });
}
