"""Render the installer splash (desktop/windows/Assets/InstallerSplash.gif).

Velopack's Setup.exe shows this GIF while it installs. It plays every frame
at one fixed delay and scales the image by the monitor's DPI, so the GIF is
authored a little above 1x (640x400) and loops seamlessly every 3 seconds:

  - the Nexus mark breathes and glows,
  - memory-graph nodes orbit it on a tilted ring (depth = size + fade),
    with gold pulses running along the edges,
  - "Setting up Nexus" with animated dots, and a slim gold shimmer that
    replaces Velopack's stock progress bar (packed with
    --splashProgressColor none).

Usage: python scripts/render-splash.py <out.gif> [frames_dir]
Needs Pillow + numpy and ffmpeg on PATH (palette pass keeps the GIF small).
"""
import math
import os
import shutil
import subprocess
import sys
import tempfile

from PIL import Image, ImageDraw, ImageFilter, ImageFont

W, H = 640, 400
SS = 2                      # supersampling
FRAMES = 90                 # 90 x 1/30 s = 3 s loop
FPS = 30
BG = (17, 19, 18)
CREAM = (242, 239, 230)
GOLD = (232, 187, 99)
MUTED = (150, 148, 140)
HERE = os.path.dirname(os.path.abspath(__file__))
LOGO = os.path.join(HERE, "..", "desktop", "windows", "Assets", "Nexus.png")
NODES = 9


def font(size, bold=False):
    names = ["SegUIVar.ttf", "segoeuisb.ttf" if bold else "segoeui.ttf", "arial.ttf"]
    for name in names:
        path = os.path.join(os.environ.get("WINDIR", r"C:\Windows"), "Fonts", name)
        if os.path.exists(path):
            f = ImageFont.truetype(path, size * SS)
            if name == "SegUIVar.ttf":
                try:
                    f.set_variation_by_name("Semibold" if bold else "Regular")
                except Exception:
                    pass
            return f
    return ImageFont.load_default()


def ease(x):
    return 0.5 - 0.5 * math.cos(2 * math.pi * x)


def background():
    bg = Image.new("RGB", (W * SS, H * SS), BG)
    glow = Image.new("L", (W * SS, H * SS), 0)
    d = ImageDraw.Draw(glow)
    cx, cy = W * SS // 2, int(H * SS * 0.42)
    d.ellipse((cx - 260 * SS, cy - 170 * SS, cx + 260 * SS, cy + 170 * SS), fill=38)
    glow = glow.filter(ImageFilter.GaussianBlur(70 * SS))
    tint = Image.new("RGB", bg.size, (40, 36, 26))
    return Image.composite(tint, bg, glow)


def main():
    out = sys.argv[1]
    keep = sys.argv[2] if len(sys.argv) > 2 else ""
    logo = Image.open(LOGO).convert("RGBA")
    title = font(19, bold=True)
    sub = font(12)
    base = background()
    cx, cy = W * SS / 2, H * SS * 0.42
    work = keep or tempfile.mkdtemp(prefix="nexus-splash-")
    os.makedirs(work, exist_ok=True)

    for i in range(FRAMES):
        t = i / FRAMES
        frame = base.copy().convert("RGBA")
        layer = Image.new("RGBA", frame.size, (0, 0, 0, 0))
        d = ImageDraw.Draw(layer)

        # Orbiting graph: rotate by one node spacing per loop => seamless.
        nodes = []
        for k in range(NODES):
            a = 2 * math.pi * (k / NODES + t / NODES) + 0.35
            r = 188 + 16 * math.sin(3 * a)
            x = cx + math.cos(a) * r * SS
            y = cy + math.sin(a) * r * 0.36 * SS
            depth = (math.sin(a) + 1) / 2            # 0 back .. 1 front
            nodes.append((x, y, depth, k))
        for x, y, depth, k in nodes:                # spokes to the mark
            alpha = int(28 + 50 * depth)
            d.line((cx, cy, x, y), fill=CREAM + (alpha,), width=SS)
        for idx in range(NODES):                    # ring edges + pulses
            x1, y1, d1, k = nodes[idx]
            x2, y2, d2, _ = nodes[(idx + 1) % NODES]
            depth = (d1 + d2) / 2
            d.line((x1, y1, x2, y2), fill=GOLD + (int(40 + 70 * depth),), width=SS)
            phase = (t * 2 + idx / NODES) % 1.0
            px, py = x1 + (x2 - x1) * phase, y1 + (y2 - y1) * phase
            pr = (2.2 + 1.6 * depth) * SS
            d.ellipse((px - pr, py - pr, px + pr, py + pr), fill=GOLD + (int(120 + 120 * depth * ease(phase)),))
        def draw_nodes(target, front):
            nd = ImageDraw.Draw(target)
            for x, y, depth, k in sorted(nodes, key=lambda n: n[2]):
                if (depth > 0.55) != front:
                    continue
                pr = (3.5 + 4.5 * depth) * SS
                twinkle = 0.75 + 0.25 * ease((t * 2 + k * 0.37) % 1.0)
                color = GOLD if k % 3 == 0 else CREAM
                nd.ellipse((x - pr, y - pr, x + pr, y + pr), fill=color + (int((90 + 165 * depth) * twinkle),))
        draw_nodes(layer, False)
        frame = Image.alpha_composite(frame, layer.filter(ImageFilter.GaussianBlur(0.6 * SS)))

        # The mark: breathe + soft gold glow (two breaths per loop).
        breath = ease((t * 2) % 1.0)
        size = int((104 + 6 * breath) * SS)
        glow = Image.new("RGBA", frame.size, (0, 0, 0, 0))
        gd = ImageDraw.Draw(glow)
        gr = (78 + 10 * breath) * SS
        gd.ellipse((cx - gr, cy - gr, cx + gr, cy + gr), fill=GOLD + (int(55 + 45 * breath),))
        frame = Image.alpha_composite(frame, glow.filter(ImageFilter.GaussianBlur(26 * SS)))
        mark = logo.resize((size, size), Image.LANCZOS)
        frame.alpha_composite(mark, (int(cx - size / 2), int(cy - size / 2)))
        front = Image.new("RGBA", frame.size, (0, 0, 0, 0))
        draw_nodes(front, True)
        frame = Image.alpha_composite(frame, front.filter(ImageFilter.GaussianBlur(0.6 * SS)))

        # Text: title with animated dots, subtitle.
        td = ImageDraw.Draw(frame)
        dots = "." * (1 + int(t * 3) % 3)
        label = "Setting up Nexus"
        lw = td.textlength(label, font=title)
        ty = int(H * SS * 0.76)
        td.text(((W * SS - lw) / 2, ty), label, font=title, fill=CREAM)
        td.text(((W * SS + lw) / 2 + 1 * SS, ty), dots, font=title, fill=GOLD)
        s = "Shared memory for your coding agents"
        sw = td.textlength(s, font=sub)
        td.text(((W * SS - sw) / 2, ty + 32 * SS), s, font=sub, fill=MUTED)

        # Indeterminate shimmer instead of the stock progress bar.
        bar_y = H * SS - 3 * SS
        td.rectangle((0, bar_y, W * SS, H * SS), fill=(34, 34, 31))
        head = (t * 1.35 - 0.2) * W * SS
        sweep = Image.new("RGBA", frame.size, (0, 0, 0, 0))
        sd = ImageDraw.Draw(sweep)
        for j in range(60):
            x = head - j * 4 * SS
            a = int(255 * (1 - j / 60) ** 2)
            sd.rectangle((x, bar_y, x + 4 * SS, H * SS), fill=GOLD + (a,))
        frame = Image.alpha_composite(frame, sweep)

        frame.convert("RGB").resize((W, H), Image.LANCZOS).save(os.path.join(work, f"f{i:03d}.png"))

    palette = os.path.join(work, "palette.png")
    pattern = os.path.join(work, "f%03d.png")
    subprocess.run(["ffmpeg", "-v", "error", "-y", "-framerate", str(FPS), "-i", pattern,
                    "-vf", "palettegen=max_colors=128:stats_mode=full", palette], check=True)
    subprocess.run(["ffmpeg", "-v", "error", "-y", "-framerate", str(FPS), "-i", pattern, "-i", palette,
                    "-lavfi", "paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle",
                    "-loop", "0", out], check=True)
    if not keep:
        shutil.rmtree(work, ignore_errors=True)
    print(out, os.path.getsize(out), "bytes")


if __name__ == "__main__":
    main()
