#!/bin/bash
# Probe macOS Now Playing metadata via standard external tools.
# Invoked by addiplay after each track switch for debugging.
# Output is captured and logged as [tmux-debug].

echo "=== now-playing probe ==="
echo "PATH=$PATH"
echo "SHELL=$SHELL"
echo "uname-r=$(uname -r)"

# 1. Check all tools the tmux-now-playing plugin tries (in priority order)
for tool in nowplaying-cli media-control playerctl mpc nc; do
    loc="$(command -v "$tool" 2>/dev/null || true)"
    if [ -n "$loc" ]; then
        echo "tool-available: $tool -> $loc"
    else
        echo "tool-missing: $tool"
    fi
done

# Also search common nix/brew paths
for dir in /opt/devcell/.local/state/nix/profiles/profile/bin \
           /run/current-system/sw/bin \
           /etc/profiles/per-user/*/bin \
           "$HOME/.nix-profile/bin" \
           /opt/homebrew/bin /usr/local/bin; do
    for tool in nowplaying-cli media-control; do
        [ -x "$dir/$tool" ] && echo "found-in-path: $dir/$tool"
    done
done 2>/dev/null

# 2. nowplaying-cli (primary backend for tmux-now-playing)
if command -v nowplaying-cli &>/dev/null; then
    echo "--- nowplaying-cli ---"
    nowplaying-cli get playbackRate title artist bundleIdentifier 2>&1
else
    echo "--- nowplaying-cli: NOT in PATH ---"
fi

# 3. media-control (second macOS backend)
if command -v media-control &>/dev/null; then
    echo "--- media-control ---"
    media-control get 2>&1
else
    echo "--- media-control: NOT in PATH ---"
fi

# 4. MPD check (port 6600)
echo "--- mpd (port 6600) ---"
if command -v nc &>/dev/null; then
    echo "status" | nc -w1 127.0.0.1 6600 2>&1 | head -5 || echo "(no mpd on 6600)"
else
    echo "(nc not available)"
fi

# 5. osascript — Music.app
echo "--- Music.app ---"
osascript -e 'try
tell application "System Events"
if exists (process "Music") then
tell application "Music" to return "title=" & name of current track & " artist=" & artist of current track & " state=" & (player state as string)
else
return "Music.app not running"
end if
end tell
on error e
return "error: " & e
end try' 2>&1

# 6. Media-related processes
echo "--- media processes ---"
pgrep -fla "mpv|Music|Spotify|addiplay" 2>&1 || echo "(none)"

# 7. Swift MediaRemote read-back — what does the system actually report?
echo "--- swift MediaRemote read-back ---"
xcrun swift -e '
import Foundation
let h = dlopen("/System/Library/PrivateFrameworks/MediaRemote.framework/MediaRemote", RTLD_LAZY)
typealias Fn = @convention(c) (DispatchQueue, @escaping (NSDictionary) -> Void) -> Void
guard let s = dlsym(h, "MRMediaRemoteGetNowPlayingInfo") else { print("dlsym FAILED"); exit(0) }
let fn = unsafeBitCast(s, to: Fn.self)
let sem = DispatchSemaphore(value: 0)
fn(DispatchQueue.main) { info in
    if info.count == 0 { print("(empty)") }
    for k in (info.allKeys as! [String]).sorted() { print("\(k)=\(info[k]!)") }
    sem.signal()
}
_ = sem.wait(timeout: .now() + 2)
' 2>&1 || echo "(swift probe failed)"

echo "=== end probe ==="
