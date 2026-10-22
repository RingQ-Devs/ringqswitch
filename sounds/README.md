# Sound prompts

`playback` and `play_and_get_digits` resolve relative file names against this
directory (setting `sounds-dir`). Any RIFF/WAVE file works: 8/16-bit PCM,
μ-law or A-law, mono or stereo, any sample rate (it is converted to 8 kHz
mono). For the best quality, use 8 kHz 16-bit mono.

Tones need no files: `tone_stream://%(on_ms,off_ms,freq1,freq2);loops=N`.
