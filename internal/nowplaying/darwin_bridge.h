//go:build darwin

#ifndef ADDIPLAY_NOWPLAYING_DARWIN_H
#define ADDIPLAY_NOWPLAYING_DARWIN_H

void NowPlayingSetup(void);
void NowPlayingUpdate(const char *artist, const char *title, int durationSec, const char *artURL);
void NowPlayingSetPlaybackState(int playing);
void NowPlayingTeardown(void);

#endif
