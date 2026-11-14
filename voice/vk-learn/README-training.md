# Voice fine-tune (layer 3) - how and when

Opt-in audio clips are stored by vk-learn in `/opt/voice/learn/audio/<user>/<dictation-id>.<ext>`
together with the final text the user sent (table `audio`). Those pairs are training data
for adapting Whisper to one person's voice.

## When
`GET /v1/vk/stats` -> `audio.fine_tune_ready` turns true at **2 hours** of clips. More is
better (5-10 h gives a clearly better result). Clips whose final text was edited a lot are
still fine: the final text is what the user actually said/meant.

## How (overnight job on the voice VM, CPU only)
`train-voice.sh <user>` (next to this file):
1. Builds a one-off training container (Python, PyTorch CPU, transformers, datasets,
   ctranslate2, ffmpeg) - nothing is installed on the VM itself.
2. Exports the user's clips + final texts to a dataset, converts audio to 16 kHz mono WAV,
   holds back 10 % for testing.
3. Fine-tunes `openai/whisper-small.en` (the base of `Systran/faster-whisper-small.en`)
   for a few epochs with a low learning rate (adapts, does not forget English).
4. Measures word error rate on the held-back clips **before and after**; keeps the new
   model only if it is better.
5. Converts it to CTranslate2 int8 (`ct2-transformers-converter`) and installs it into
   speaches' model cache as `local/whisper-small.en-<user>`.
6. The app then offers that model in the speech-model picker (it is listed in
   GET /v1/models); switching back is just picking `Systran/faster-whisper-small.en`.

Expected time on 16 vCPUs: roughly 1-3 hours per hour of audio. It uses all cores, so it
runs at night (dictation keeps working, just slower while it runs).

## Status
Collection is live (2026-11-15). The training script is written but has not been run:
there is no audio yet. Test it on the first 2 hours of clips before relying on it.
