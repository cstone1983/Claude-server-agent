# Voice gateway helper (container voice-models-merge, port 8081):
#  GET  /v1/models                -> speaches (speech-to-text) + Ollama (cleanup) model lists in one
#                                    OpenAI-style answer; each entry gets "task": "transcription"/"chat".
#  POST /v1/audio/transcriptions  -> speaches, with a model fallback: if the requested model is not
#                                    installed (e.g. a person's fine-tuned model that is not there yet)
#                                    or fails to load, the default model is used instead of an error.
#                                    Only the multipart "model" value is changed; every other byte
#                                    (audio, prompt, language, ...) is passed through unchanged.
# Nothing here logs request bodies.
import json, os, re, time, urllib.error, urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SPEACHES = os.environ.get("STT_URL", "http://speaches:8000")
OLLAMA = os.environ.get("CHAT_URL", "http://ollama:11434")
DEFAULT_STT = os.environ.get("STT_DEFAULT", "Systran/faster-whisper-small.en")
MAX_BODY = 50 * 1024 * 1024
_cache = {"t": 0.0, "ids": set()}


def fetch(url):
    try:
        with urllib.request.urlopen(url, timeout=5) as r:
            return json.load(r).get("data", [])
    except Exception:
        return []


def installed():
    """Speech models speaches has on disk (cached 60 s; empty set = unknown)."""
    if time.time() - _cache["t"] > 60:
        ids = {m.get("id") for m in fetch(SPEACHES + "/v1/models") if m.get("id")}
        if ids:
            _cache.update(t=time.time(), ids=ids)
    return _cache["ids"]


MODEL_PART = re.compile(rb'(Content-Disposition:[^\r\n]*name="model"[^\r\n]*\r\n(?:[^\r\n]+\r\n)*\r\n)(.*?)(\r\n--)',
                        re.S | re.I)


def get_model(body):
    m = MODEL_PART.search(body)
    return m.group(2).decode("utf-8", "replace").strip() if m else None


def set_model(body, model):
    return MODEL_PART.sub(lambda m: m.group(1) + model.encode() + m.group(3), body, count=1)


class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def send(self, code, body, ctype="application/json", extra=None):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.split("?")[0] != "/v1/models":
            return self.send(404, b'{"error":"not found"}')
        data = []
        for m in fetch(SPEACHES + "/v1/models"):
            m["task"] = "transcription"; data.append(m)
        for m in fetch(OLLAMA + "/v1/models"):
            m["task"] = "chat"; data.append(m)
        self.send(200, json.dumps({"object": "list", "data": data}).encode())

    def forward(self, body):
        req = urllib.request.Request(SPEACHES + self.path, data=body, method="POST",
                                     headers={"Content-Type": self.headers.get("Content-Type", "")})
        try:
            with urllib.request.urlopen(req, timeout=300) as r:
                return r.status, r.read(), r.headers.get("Content-Type", "application/json")
        except urllib.error.HTTPError as e:
            return e.code, e.read(), e.headers.get("Content-Type", "application/json")

    def do_POST(self):
        if self.path.split("?")[0] != "/v1/audio/transcriptions":
            return self.send(404, b'{"error":"not found"}')
        n = int(self.headers.get("Content-Length") or 0)
        if n > MAX_BODY:
            self.close_connection = True
            return self.send(413, b'{"error":"audio too large"}')
        body = self.rfile.read(n) if n else b""
        asked = get_model(body)
        fell_back = ""
        ids = installed()
        if asked and asked != DEFAULT_STT and ids and asked not in ids:
            body, fell_back = set_model(body, DEFAULT_STT), asked          # not installed: never try to download it
        try:
            code, out, ctype = self.forward(body)
        except Exception as e:
            print(f"transcription: speaches unreachable: {type(e).__name__}", flush=True)
            return self.send(502, b'{"error":"speech server unreachable"}')
        if code >= 400 and asked and not fell_back and asked != DEFAULT_STT:
            # e.g. the person's model is listed but cannot be loaded: retry with the default
            body, fell_back = set_model(body, DEFAULT_STT), asked
            code, out, ctype = self.forward(body)
        extra = {}
        if fell_back:
            extra["X-VK-STT-Fallback"] = fell_back
            print(f"transcription: model {fell_back!r} not usable, used {DEFAULT_STT} (status {code})", flush=True)
        self.send(code, out, ctype, extra)

    def log_message(self, *a):
        pass


ThreadingHTTPServer(("0.0.0.0", 8081), H).serve_forever()
