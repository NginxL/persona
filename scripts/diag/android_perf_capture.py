"""Capture commands and parse frame counts for the Android performance scripts."""
import re
import subprocess


def adb_command(serial, *args, timeout, capture=False):
    command = ['adb', '-s', serial, *args]
    try:
        return subprocess.run(command, stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
                              stderr=subprocess.DEVNULL, text=True, timeout=timeout, check=True).stdout
    except subprocess.CalledProcessError as error:
        raise SystemExit(f"adb {' '.join(args[:2])} failed (exit {error.returncode})") from None


def frame_counts(text):
    total = re.search(r'Total frames rendered: (\d+)', text)
    janky = re.search(r'Janky frames: (\d+)', text)
    if total and janky:
        frames, janky_frames = int(total[1]), int(janky[1])
        if 0 < frames and janky_frames <= frames:
            return frames, janky_frames
    raise SystemExit('gfxinfo did not report a usable frame sample')
