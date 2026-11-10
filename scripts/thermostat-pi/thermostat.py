# -*- coding: utf-8 -*-
"""Basement thermostat / temperature sensor (Raspberry Pi 5, DHT22, relay).

Publishes temperature, humidity, mode, target and action to Home Assistant over
MQTT and follows HA's mode/target commands. The relay is currently unwired
(sensor use only); the control logic is kept and fails safe (relay off).

Settings come from the environment (systemd EnvironmentFile):
  MQTT_HOST (default homeassistant.localdomain), MQTT_PORT (1883),
  MQTT_USER, MQTT_PASS, LOCATION (Basement)
State (mode, target) is saved to state.json next to this file, so a restart
keeps what HA last set instead of falling back to a default.
"""
import json
import os
import signal
import sys
import time

import adafruit_dht
import board
import digitalio
import paho.mqtt.client as mqtt

# --- CONFIGURATION ---
RELAY_PIN = board.D27  # physical pin 13
DHT_PIN = board.D4     # physical pin 7
LOCATION = os.environ.get("LOCATION", "Basement")
MQTT_HOST = os.environ.get("MQTT_HOST", "homeassistant.localdomain")
MQTT_PORT = int(os.environ.get("MQTT_PORT", "1883"))
MQTT_USER = os.environ.get("MQTT_USER", "")
MQTT_PASS = os.environ.get("MQTT_PASS", "")

LOOP_SECONDS = 10
HYSTERESIS = 1.0
VALID_MODES = ("off", "heat")
TARGET_MIN, TARGET_MAX = 40.0, 85.0     # accepted target range, F
TEMP_MIN_C, TEMP_MAX_C = -20.0, 60.0    # plausible sensor range, C
MAX_JUMP_F = 10.0                       # reject a reading this far from the last good one
STALE_AFTER = 300                       # seconds without a good reading -> relay off, "idle"

STATE_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "state.json")

# MQTT topics (unchanged, as configured in Home Assistant)
TOPIC_MODE_SET = "thermostat/mode/set"
TOPIC_TEMP_SET = "thermostat/temp/set"
TOPIC_MODE_STATE = "thermostat/mode/state"
TOPIC_TEMP_STATE = "thermostat/temp/state"
TOPIC_CURRENT_TEMP = "thermostat/current_temp"
TOPIC_HUMIDITY = "thermostat/humidity"
TOPIC_ACTION = "thermostat/action"
TOPIC_AVAILABILITY = "thermostat/availability"   # "online" / "offline" (broker sends offline if we vanish)


def log(msg):
    print(msg, flush=True)


# --- STATE (survives restarts) ---
def load_state():
    state = {"mode": "off", "target": 68.0}
    try:
        with open(STATE_FILE) as f:
            saved = json.load(f)
        if saved.get("mode") in VALID_MODES:
            state["mode"] = saved["mode"]
        t = float(saved.get("target", state["target"]))
        if TARGET_MIN <= t <= TARGET_MAX:
            state["target"] = t
    except FileNotFoundError:
        pass
    except Exception as e:
        log(f"State file unreadable, using defaults: {e}")
    return state


def save_state():
    tmp = STATE_FILE + ".tmp"
    try:
        with open(tmp, "w") as f:
            json.dump(state, f)
        os.replace(tmp, STATE_FILE)
    except Exception as e:
        log(f"Could not save state: {e}")


state = load_state()

# --- HARDWARE ---
os.system("pkill -f libgpiod_pulsein || true")
relay = digitalio.DigitalInOut(RELAY_PIN)
relay.direction = digitalio.Direction.OUTPUT
relay.value = False

dht_device = None


def init_sensor():
    global dht_device
    try:
        if dht_device is not None:
            dht_device.exit()
    except Exception:
        pass
    try:
        dht_device = adafruit_dht.DHT22(DHT_PIN)
    except Exception as e:
        log(f"Sensor init error: {e}")
        dht_device = None


init_sensor()


def read_sensor(last_good_f):
    """One DHT22 reading as (temp_f, humidity) or (None, None). Rejects
    impossible values and sudden jumps (a known DHT22 glitch)."""
    if dht_device is None:
        init_sensor()
        return None, None
    try:
        t, h = dht_device.temperature, dht_device.humidity
    except RuntimeError:
        return None, None            # checksum/timing errors are normal for a DHT22
    except Exception as e:
        log(f"Sensor error, re-initialising: {e}")
        init_sensor()
        return None, None
    if t is None or not (TEMP_MIN_C <= t <= TEMP_MAX_C):
        return None, None
    temp_f = t * 9.0 / 5.0 + 32.0
    if last_good_f is not None and abs(temp_f - last_good_f) > MAX_JUMP_F:
        log(f"Ignoring jump {last_good_f:.1f} -> {temp_f:.1f} F")
        return None, None
    hum = h if h is not None and 0 <= h <= 100 else None
    return temp_f, hum


# --- MQTT ---
def on_connect(client, userdata, flags, reason_code, properties):
    if reason_code == 0:
        log(f"Connected to MQTT broker {MQTT_HOST}")
        client.subscribe([(TOPIC_MODE_SET, 1), (TOPIC_TEMP_SET, 1)])
        client.publish(TOPIC_AVAILABILITY, "online", qos=1, retain=True)
        client.publish(TOPIC_MODE_STATE, state["mode"], qos=1, retain=True)
        client.publish(TOPIC_TEMP_STATE, state["target"], qos=1, retain=True)
    else:
        log(f"MQTT connect refused: {reason_code}")


def on_disconnect(client, userdata, flags, reason_code, properties):
    if reason_code != 0:
        log(f"MQTT disconnected ({reason_code}); reconnecting automatically")


def on_message(client, userdata, msg):
    payload = msg.payload.decode("utf-8", "replace").strip()
    try:
        if msg.topic == TOPIC_TEMP_SET:
            t = float(payload)
            if not (TARGET_MIN <= t <= TARGET_MAX):
                log(f"Ignoring target {payload} (outside {TARGET_MIN}-{TARGET_MAX})")
                return
            state["target"] = t
            save_state()
            client.publish(TOPIC_TEMP_STATE, t, qos=1, retain=True)
        elif msg.topic == TOPIC_MODE_SET:
            mode = payload.lower()
            if mode not in VALID_MODES:
                log(f"Ignoring unknown mode {payload!r}")
                return
            state["mode"] = mode
            save_state()
            client.publish(TOPIC_MODE_STATE, mode, qos=1, retain=True)
    except ValueError:
        log(f"Ignoring bad payload on {msg.topic}: {payload!r}")


client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2, client_id=f"thermostat-{LOCATION.lower()}")
if MQTT_USER:
    client.username_pw_set(MQTT_USER, MQTT_PASS)
client.will_set(TOPIC_AVAILABILITY, "offline", qos=1, retain=True)
client.on_connect = on_connect
client.on_disconnect = on_disconnect
client.on_message = on_message
client.reconnect_delay_set(min_delay=2, max_delay=60)
# connect_async + loop_start: keeps retrying in the background even if the
# broker is not reachable at start-up (e.g. the Pi boots before Home Assistant)
client.connect_async(MQTT_HOST, MQTT_PORT, keepalive=60)
client.loop_start()


def shutdown(signum, frame):
    relay.value = False
    try:
        client.publish(TOPIC_AVAILABILITY, "offline", qos=1, retain=True).wait_for_publish(2)
    except Exception:
        pass
    client.disconnect()
    client.loop_stop()
    log("Stopped; relay off")
    sys.exit(0)


signal.signal(signal.SIGTERM, shutdown)
signal.signal(signal.SIGINT, shutdown)


# --- MAIN LOOP ---
def control_loop():
    log(f"--- {LOCATION} thermostat active (mode {state['mode']}, target {state['target']}) ---")
    last_good_f, last_good_at = None, 0.0
    while True:
        temp_f, hum = read_sensor(last_good_f)
        now = time.monotonic()
        if temp_f is not None:
            last_good_f, last_good_at = temp_f, now
        fresh = last_good_f is not None and now - last_good_at <= STALE_AFTER

        if not fresh or state["mode"] == "off":
            relay.value = False             # failsafe: no recent reading, or off
            action = "idle"
        else:
            target = state["target"]
            if not relay.value and last_good_f < target - HYSTERESIS:
                relay.value = True
            elif relay.value and last_good_f > target + HYSTERESIS:
                relay.value = False
            action = "heating" if relay.value else "idle"

        if temp_f is not None:
            client.publish(TOPIC_CURRENT_TEMP, round(temp_f, 1), retain=True)
            if hum is not None:
                client.publish(TOPIC_HUMIDITY, round(hum, 1), retain=True)
        client.publish(TOPIC_ACTION, action, retain=True)
        time.sleep(LOOP_SECONDS)


if __name__ == "__main__":
    control_loop()
