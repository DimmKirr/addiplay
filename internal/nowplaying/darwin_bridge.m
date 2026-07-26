//go:build darwin

#import <Foundation/Foundation.h>
#import <AppKit/NSImage.h>
#import <MediaPlayer/MediaPlayer.h>
#import <dlfcn.h>
#import "darwin_bridge.h"

extern void goNowPlayingPlayPause(void);
extern void goNowPlayingNext(void);
extern void goNowPlayingPrevious(void);
extern void goNowPlayingLog(const char *msg);

static void nplog(NSString *fmt, ...) __attribute__((format(__NSString__, 1, 2)));
static void nplog(NSString *fmt, ...) {
    va_list args;
    va_start(args, fmt);
    NSString *s = [[NSString alloc] initWithFormat:fmt arguments:args];
    va_end(args);
    goNowPlayingLog([s UTF8String]);
}

// ---------------------------------------------------------------------------
// MediaRemote private framework — the only reliable way for a non-audio
// process to claim the Now Playing slot on macOS.
// ---------------------------------------------------------------------------
static void *mrHandle = NULL;

typedef void (*MRSetCanBeNowPlaying_t)(BOOL);
typedef void (*MRSetNowPlayingInfo_t)(CFDictionaryRef);
typedef void (*MRSetIsPlaying_t)(BOOL);
static MRSetCanBeNowPlaying_t   fnSetCanBe   = NULL;
static MRSetNowPlayingInfo_t    fnSetInfo    = NULL;
static MRSetIsPlaying_t         fnSetIsPlaying = NULL;

// MediaRemote key symbols — loaded at runtime via dlsym.
static NSString *kTitle        = nil;
static NSString *kArtist       = nil;
static NSString *kAlbum        = nil;
static NSString *kPlaybackRate = nil;
static NSString *kMediaType    = nil;
static NSString *kElapsedTime  = nil;
static NSString *kDuration     = nil;
static NSString *kArtworkData  = nil;
static NSString *kArtworkMIME  = nil;

static BOOL loadMediaRemote(void) {
    if (mrHandle) return YES;
    mrHandle = dlopen(
        "/System/Library/PrivateFrameworks/MediaRemote.framework/MediaRemote",
        RTLD_LAZY);
    if (!mrHandle) {
        nplog(@"loadMediaRemote: dlopen FAILED: %s", dlerror());
        return NO;
    }

    fnSetCanBe = (MRSetCanBeNowPlaying_t)dlsym(mrHandle,
        "MRMediaRemoteSetCanBeNowPlayingApplication");
    fnSetInfo = (MRSetNowPlayingInfo_t)dlsym(mrHandle,
        "MRMediaRemoteSetNowPlayingInfo");
    fnSetIsPlaying = (MRSetIsPlaying_t)dlsym(mrHandle,
        "MRMediaRemoteSetNowPlayingApplicationIsPlaying");
    if (!fnSetCanBe || !fnSetInfo) {
        nplog(@"loadMediaRemote: dlsym FAILED (canBe=%p info=%p isPlaying=%p)",
              fnSetCanBe, fnSetInfo, fnSetIsPlaying);
        return NO;
    }

    NSString **p;
    p = dlsym(mrHandle, "kMRMediaRemoteNowPlayingInfoTitle");
    if (p) kTitle = *p;
    p = dlsym(mrHandle, "kMRMediaRemoteNowPlayingInfoArtist");
    if (p) kArtist = *p;
    p = dlsym(mrHandle, "kMRMediaRemoteNowPlayingInfoAlbum");
    if (p) kAlbum = *p;
    p = dlsym(mrHandle, "kMRMediaRemoteNowPlayingInfoPlaybackRate");
    if (p) kPlaybackRate = *p;
    p = dlsym(mrHandle, "kMRMediaRemoteNowPlayingInfoMediaType");
    if (p) kMediaType = *p;
    p = dlsym(mrHandle, "kMRMediaRemoteNowPlayingInfoElapsedTime");
    if (p) kElapsedTime = *p;
    p = dlsym(mrHandle, "kMRMediaRemoteNowPlayingInfoDuration");
    if (p) kDuration = *p;
    p = dlsym(mrHandle, "kMRMediaRemoteNowPlayingInfoArtworkData");
    if (p) kArtworkData = *p;
    p = dlsym(mrHandle, "kMRMediaRemoteNowPlayingInfoArtworkMIMEType");
    if (p) kArtworkMIME = *p;

    nplog(@"loadMediaRemote: OK (title=%@ artist=%@ rate=%@ mediaType=%@ elapsed=%@ duration=%@ artwork=%@ isPlaying=%p)",
          kTitle ?: @"(nil)", kArtist ?: @"(nil)", kPlaybackRate ?: @"(nil)",
          kMediaType ?: @"(nil)", kElapsedTime ?: @"(nil)", kDuration ?: @"(nil)",
          kArtworkData ?: @"(nil)", fnSetIsPlaying);
    return YES;
}

// Cached info dict — updated incrementally by Update / SetPlaybackState.
static NSMutableDictionary *cachedInfo = nil;
static BOOL currentlyPlaying = NO;
static NSDate *trackStartTime = nil;
static NSString *lastArtworkURL = nil;

static void pushInfo(void) {
    if (!fnSetInfo || !cachedInfo) return;
    if (fnSetCanBe) fnSetCanBe(YES);
    fnSetInfo((__bridge CFDictionaryRef)cachedInfo);
    nplog(@"pushInfo (re-asserted SetCanBe): %@", cachedInfo);
}

// ---------------------------------------------------------------------------
// Artwork download — fetches image from URL on a background queue, then
// pushes artwork data into both MediaRemote and MPNowPlayingInfoCenter
// on the main thread.
// ---------------------------------------------------------------------------
static void fetchAndSetArtwork(NSString *urlString) {
    if (urlString.length == 0) return;

    NSURL *url = [NSURL URLWithString:urlString];
    if (!url) {
        nplog(@"fetchAndSetArtwork: invalid URL: %@", urlString);
        return;
    }

    NSURLSession *session = [NSURLSession sharedSession];
    [[session dataTaskWithURL:url completionHandler:^(NSData *data, NSURLResponse *response, NSError *error) {
        if (error || !data || data.length == 0) {
            nplog(@"fetchAndSetArtwork: download failed url=%@ err=%@", urlString, error);
            return;
        }
        NSHTTPURLResponse *httpResp = (NSHTTPURLResponse *)response;
        if (httpResp.statusCode != 200) {
            nplog(@"fetchAndSetArtwork: HTTP %ld url=%@", (long)httpResp.statusCode, urlString);
            return;
        }

        nplog(@"fetchAndSetArtwork: downloaded %lu bytes from %@", (unsigned long)data.length, urlString);

        dispatch_async(dispatch_get_main_queue(), ^{
            // Guard: if the URL changed while we were downloading, discard.
            if (![lastArtworkURL isEqualToString:urlString]) {
                nplog(@"fetchAndSetArtwork: stale download (wanted %@, now %@)", urlString, lastArtworkURL);
                return;
            }

            // MediaRemote private API
            if (cachedInfo && fnSetInfo) {
                if (kArtworkData) cachedInfo[kArtworkData] = data;
                if (kArtworkMIME) {
                    NSString *mime = httpResp.MIMEType ?: @"image/jpeg";
                    cachedInfo[kArtworkMIME] = mime;
                }
                pushInfo();
            }

            // MPNowPlayingInfoCenter fallback
            NSImage *image = [[NSImage alloc] initWithData:data];
            if (image) {
                MPNowPlayingInfoCenter *center = [MPNowPlayingInfoCenter defaultCenter];
                NSMutableDictionary *info = [center.nowPlayingInfo mutableCopy] ?: [NSMutableDictionary new];
                info[MPMediaItemPropertyArtwork] = [[MPMediaItemArtwork alloc]
                    initWithBoundsSize:image.size
                    requestHandler:^NSImage *(CGSize size) {
                        return image;
                    }];
                center.nowPlayingInfo = info;
                nplog(@"fetchAndSetArtwork: artwork set (%@, %.0fx%.0f)",
                      urlString, image.size.width, image.size.height);
            } else {
                nplog(@"fetchAndSetArtwork: NSImage decode failed for %@", urlString);
            }
        });
    }] resume];
}

// ---------------------------------------------------------------------------
// Public C functions called from Go
// ---------------------------------------------------------------------------

void NowPlayingSetup(void) {
    nplog(@"NowPlayingSetup called (isMainThread=%d)", [NSThread isMainThread]);
    dispatch_async(dispatch_get_main_queue(), ^{
        nplog(@"NowPlayingSetup executing on main thread");

        if (!loadMediaRemote()) {
            nplog(@"NowPlayingSetup: MediaRemote unavailable, falling back to MPNowPlayingInfoCenter");
        }

        if (fnSetCanBe) {
            fnSetCanBe(YES);
            nplog(@"NowPlayingSetup: MRMediaRemoteSetCanBeNowPlayingApplication(YES)");
        }

        cachedInfo = [NSMutableDictionary new];

        // Still register MPRemoteCommandCenter handlers — they handle
        // media key / AirPods / Control Center button presses.
        MPRemoteCommandCenter *cc = [MPRemoteCommandCenter sharedCommandCenter];

        [cc.togglePlayPauseCommand setEnabled:YES];
        [cc.togglePlayPauseCommand addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
            nplog(@"togglePlayPauseCommand fired");
            goNowPlayingPlayPause();
            return MPRemoteCommandHandlerStatusSuccess;
        }];

        [cc.nextTrackCommand setEnabled:YES];
        [cc.nextTrackCommand addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
            nplog(@"nextTrackCommand fired");
            goNowPlayingNext();
            return MPRemoteCommandHandlerStatusSuccess;
        }];

        [cc.previousTrackCommand setEnabled:YES];
        [cc.previousTrackCommand addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
            nplog(@"previousTrackCommand fired");
            goNowPlayingPrevious();
            return MPRemoteCommandHandlerStatusSuccess;
        }];
        nplog(@"NowPlayingSetup complete — commands registered");
    });
}

void NowPlayingUpdate(const char *artist, const char *title, int durationSec, const char *artURL) {
    NSString *nsArtist = artist ? [NSString stringWithUTF8String:artist] : @"";
    NSString *nsTitle  = title  ? [NSString stringWithUTF8String:title]  : @"";
    NSString *nsArtURL = (artURL && artURL[0]) ? [NSString stringWithUTF8String:artURL] : nil;
    double dur = durationSec > 0 ? (double)durationSec : 300.0;

    nplog(@"NowPlayingUpdate called artist=%@ title=%@ duration=%d artURL=%@ (isMainThread=%d)",
          nsArtist, nsTitle, durationSec, nsArtURL ?: @"(none)", [NSThread isMainThread]);
    dispatch_async(dispatch_get_main_queue(), ^{
        trackStartTime = [NSDate date];

        if (cachedInfo && fnSetInfo) {
            if (kArtist) cachedInfo[kArtist] = nsArtist;
            if (kTitle)  cachedInfo[kTitle]  = nsTitle;
            if (kPlaybackRate) cachedInfo[kPlaybackRate] = @(currentlyPlaying ? 1.0 : 0.0);
            if (kMediaType) cachedInfo[kMediaType] = @(1);
            if (kElapsedTime) cachedInfo[kElapsedTime] = @(0.0);
            if (kDuration) cachedInfo[kDuration] = @(dur);
            pushInfo();
        }

        // Also set via MPNowPlayingInfoCenter as a belt-and-suspenders fallback.
        MPNowPlayingInfoCenter *center = [MPNowPlayingInfoCenter defaultCenter];
        NSMutableDictionary *info = [center.nowPlayingInfo mutableCopy] ?: [NSMutableDictionary new];
        info[MPMediaItemPropertyArtist] = nsArtist;
        info[MPMediaItemPropertyTitle]  = nsTitle;
        info[MPMediaItemPropertyPlaybackDuration] = @(dur);
        info[MPNowPlayingInfoPropertyElapsedPlaybackTime] = @(0.0);
        if (!info[MPNowPlayingInfoPropertyPlaybackRate]) {
            info[MPNowPlayingInfoPropertyPlaybackRate] = @(1.0);
        }
        center.nowPlayingInfo = info;
        nplog(@"NowPlayingUpdate applied (MPInfoCenter fallback): info=%@, playbackState=%ld",
              info, (long)center.playbackState);

        // Kick off async artwork download if the URL changed.
        if (nsArtURL && ![nsArtURL isEqualToString:lastArtworkURL]) {
            lastArtworkURL = [nsArtURL copy];
            fetchAndSetArtwork(nsArtURL);
        } else if (!nsArtURL) {
            lastArtworkURL = nil;
        }
    });
}

void NowPlayingSetPlaybackState(int playing) {
    nplog(@"NowPlayingSetPlaybackState called playing=%d (isMainThread=%d)",
          playing, [NSThread isMainThread]);
    dispatch_async(dispatch_get_main_queue(), ^{
        currentlyPlaying = playing;

        if (fnSetIsPlaying) {
            fnSetIsPlaying(playing ? YES : NO);
            nplog(@"MRMediaRemoteSetNowPlayingApplicationIsPlaying(%d)", playing);
        }

        if (cachedInfo && fnSetInfo) {
            if (kPlaybackRate) cachedInfo[kPlaybackRate] = @(playing ? 1.0 : 0.0);
            pushInfo();
        }

        // MPNowPlayingInfoCenter fallback
        MPNowPlayingInfoCenter *center = [MPNowPlayingInfoCenter defaultCenter];
        center.playbackState = playing
            ? MPNowPlayingPlaybackStatePlaying
            : MPNowPlayingPlaybackStatePaused;

        NSMutableDictionary *info = [center.nowPlayingInfo mutableCopy] ?: [NSMutableDictionary new];
        info[MPNowPlayingInfoPropertyPlaybackRate] = @(playing ? 1.0 : 0.0);
        center.nowPlayingInfo = info;

        nplog(@"NowPlayingSetPlaybackState applied: playing=%d state=%ld",
              playing, (long)center.playbackState);
    });
}

void NowPlayingTeardown(void) {
    nplog(@"NowPlayingTeardown called (isMainThread=%d)", [NSThread isMainThread]);
    dispatch_async(dispatch_get_main_queue(), ^{
        MPRemoteCommandCenter *cc = [MPRemoteCommandCenter sharedCommandCenter];
        [cc.togglePlayPauseCommand removeTarget:nil];
        [cc.nextTrackCommand removeTarget:nil];
        [cc.previousTrackCommand removeTarget:nil];

        [MPNowPlayingInfoCenter defaultCenter].nowPlayingInfo = nil;

        if (fnSetInfo) {
            fnSetInfo((__bridge CFDictionaryRef)@{});
        }
        if (fnSetCanBe) {
            fnSetCanBe(NO);
        }

        cachedInfo = nil;
        nplog(@"NowPlayingTeardown complete");
    });
}
