#!/bin/bash
# train-voice.sh <user> - fine-tune Whisper small.en on one person's stored clips.
# See README-training.md. Runs in a throwaway container; output goes to speaches'
# model cache as local/whisper-small.en-<user>. UNTESTED until real clips exist.
set -euo pipefail
U="${1:?usage: train-voice.sh <user>}"
DATA=/opt/voice/learn; HF=/opt/voice/hf-cache; WORK=/opt/voice/train/$U
mkdir -p "$WORK"
python3 - "$U" "$DATA" "$WORK" <<'PY'
import json, sqlite3, sys
u, data, work = sys.argv[1:4]
c = sqlite3.connect(f"file:{data}/learn.db?mode=ro", uri=True)
rows = c.execute("SELECT path, text FROM audio WHERE user=? AND text != ''", (u,)).fetchall()
with open(f"{work}/clips.jsonl", "w") as f:
    for p, t in rows:
        f.write(json.dumps({"audio": p.replace("/data/", f"{data}/"), "text": t}) + "\n")
print(f"{len(rows)} clips exported")
PY
cat > "$WORK/train.py" <<'PY'
import json, random, sys, subprocess, os
import torch, evaluate, numpy as np, soundfile as sf
from transformers import WhisperProcessor, WhisperForConditionalGeneration, Seq2SeqTrainer, Seq2SeqTrainingArguments
work = sys.argv[1]
rows = [json.loads(l) for l in open(f"{work}/clips.jsonl")]
os.makedirs(f"{work}/wav", exist_ok=True)
for i, r in enumerate(rows):
    out = f"{work}/wav/{i}.wav"
    subprocess.run(["ffmpeg", "-loglevel", "error", "-y", "-i", r["audio"], "-ac", "1", "-ar", "16000", out], check=True)
    r["wav"] = out
random.seed(1); random.shuffle(rows)
n_test = max(1, len(rows) // 10); test, train = rows[:n_test], rows[n_test:]
base = "openai/whisper-small.en"
proc = WhisperProcessor.from_pretrained(base); model = WhisperForConditionalGeneration.from_pretrained(base)
torch.set_num_threads(os.cpu_count())
def feats(r):
    a, sr = sf.read(r["wav"]); x = proc.feature_extractor(a, sampling_rate=16000).input_features[0]
    return {"input_features": x, "labels": proc.tokenizer(r["text"]).input_ids}
tr = [feats(r) for r in train]
def collate(b):
    x = proc.feature_extractor.pad([{"input_features": e["input_features"]} for e in b], return_tensors="pt")
    y = proc.tokenizer.pad([{"input_ids": e["labels"]} for e in b], return_tensors="pt")
    lab = y["input_ids"].masked_fill(y.attention_mask.ne(1), -100)
    if (lab[:, 0] == model.config.decoder_start_token_id).all(): lab = lab[:, 1:]
    x["labels"] = lab; return x
wer = evaluate.load("wer")
def score(m):
    hyp = []
    for r in test:
        a, sr = sf.read(r["wav"]); x = proc(a, sampling_rate=16000, return_tensors="pt").input_features
        hyp.append(proc.batch_decode(m.generate(x), skip_special_tokens=True)[0])
    return wer.compute(predictions=hyp, references=[r["text"] for r in test])
before = score(model); print(f"WER before: {before:.3f}", flush=True)
args = Seq2SeqTrainingArguments(output_dir=f"{work}/out", per_device_train_batch_size=8, learning_rate=1e-5,
    num_train_epochs=3, save_strategy="no", logging_steps=10, report_to=[], use_cpu=True)
Seq2SeqTrainer(model=model, args=args, train_dataset=tr, data_collator=collate).train()
after = score(model); print(f"WER after: {after:.3f}", flush=True)
json.dump({"before": before, "after": after, "clips": len(rows)}, open(f"{work}/result.json", "w"))
if after < before:
    model.save_pretrained(f"{work}/hf"); proc.save_pretrained(f"{work}/hf"); print("IMPROVED: saved")
else:
    print("NOT better: keeping the current model")
PY
docker run --rm -v "$WORK:$WORK" -v "$DATA:$DATA:ro" -w "$WORK" python:3.12-slim bash -c "
  apt-get update -qq && apt-get install -y -qq ffmpeg >/dev/null &&
  pip install -q --index-url https://download.pytorch.org/whl/cpu torch &&
  pip install -q transformers accelerate evaluate jiwer soundfile ctranslate2 &&
  python train.py $WORK &&
  if [ -d $WORK/hf ]; then ct2-transformers-converter --model $WORK/hf --output_dir $WORK/ct2 --quantization int8 --copy_files tokenizer.json preprocessor_config.json; fi"
if [ -d "$WORK/ct2" ]; then
  dest="$HF/models--local--whisper-small.en-$U"
  rm -rf "$dest"; mkdir -p "$dest/snapshots/main"; cp -r "$WORK/ct2/." "$dest/snapshots/main/"
  echo main > "$dest/refs-main" 2>/dev/null || true; mkdir -p "$dest/refs"; echo main > "$dest/refs/main"
  chown -R 1000:1000 "$dest"
  echo "Installed local/whisper-small.en-$U - restart speaches and check GET /v1/models"
fi
cat "$WORK/result.json" 2>/dev/null || true
